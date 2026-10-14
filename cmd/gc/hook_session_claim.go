package main

import (
	"io"
	"log"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/session"
)

// sessionCurrentClaimFrontDoor opens the routed session-class write front door
// for the city this process is running in. It is the shared root for the two
// halves of the claim back-channel: `gc hook --claim` stamps the claimed bead id
// onto the calling session's bead through it, and `gc hook current` reads that
// stamp back through it.
//
// It routes through cliSessionFrontDoor so a [beads.classes.sessions] relocation
// reaches both halves — a raw work-store front door would stamp the claim onto
// the work store while the real session bead lives in the relocated store, and
// `gc hook current` would then read back nothing forever. The no-refresh config
// loader matches the other hook-path roots (cmd_prime.go's
// persistPrimeHookProviderSessionKey): this runs on every claim, and a nil cfg
// leaves cliSessionStore identity to the input store.
func sessionCurrentClaimFrontDoor() (*session.Store, error) {
	cityPath, err := resolveCity()
	if err != nil {
		return nil, err
	}
	store, err := openCityStoreAt(cityPath)
	if err != nil {
		return nil, err
	}
	cfg, _ := loadCityConfigWithoutBuiltinPackRefresh(cityPath, io.Discard)
	return cliSessionFrontDoor(store, cfg, cityPath), nil
}

// hookStampSessionCurrentClaim records beadID as the work bead the session
// identified by sessionID is currently running. It is the production
// implementation of the hookClaimOps.StampSessionClaim seam.
//
// The write goes through session.Store.SetCurrentClaim, which resolves the id
// EXACTLY and refuses a non-session bead before writing anything: bd's fuzzy id
// resolver would otherwise let a post-claim update land on a prefix-colliding
// session if the intended one disappeared concurrently, which is why the claim
// path decorates the session bead only through this guarded seam (see
// publishHookClaimRunMap, which stays a file-based sidecar for exactly that
// reason). SetCurrentClaim also compare-and-skips, so the per-tick adoption
// re-run issues no write once the value is current.
func hookStampSessionCurrentClaim(sessionID, beadID string) error {
	sessFront, err := sessionCurrentClaimFrontDoor()
	if err != nil {
		return err
	}
	_, err = sessFront.SetCurrentClaim(sessionID, beadID)
	return err
}

// hookSessionDrainPending reports whether the session identified by sessionID is
// already draining. It is the production implementation of the
// hookClaimOps.DrainPending seam — the F-D claim fence's only input.
//
// The SESSION ROW is the source, not provider meta. `gc runtime drain-check`
// reads GC_DRAIN, which reconciler-tracked drains never set, so a provider-meta
// probe would miss the whole keyed population this fence exists for. It reads
// through the same routed front door the claim back-channel writes through, so a
// [beads.classes.sessions] relocation reaches the fence too.
//
// Any state OTHER than draining — including a closed row, whose runtime state
// GetState reports as empty — is not this fence's business. A closed or
// superseded incarnation is the runtime-identity fence's stale-session lane, and
// answering false here leaves that lane's verdict intact rather than relabelling
// it. Errors are returned rather than swallowed: the caller fails OPEN on them,
// and it can only make that choice if it can see them.
func hookSessionDrainPending(sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	sessFront, err := sessionCurrentClaimFrontDoor()
	if err != nil {
		return false, err
	}
	state, _, err := sessFront.GetState(sessionID)
	if err != nil {
		return false, err
	}
	return state == session.StateDraining, nil
}

// hookResolveSessionWorkDir returns the checkout the session identified by
// sessionID is running in, or "" when that is not knowable. It is the production
// implementation of the hookClaimOps.ResolveSessionWorkDir seam.
//
// A bead routed to a POOL records no checkout of its own: cliBeadRouter.Route
// normalizes a pool target through agentutil.NormalizePoolRouteTarget, so
// gc.routed_to names a pool identity and no slot exists to name a worktree until
// a claim happens (gc-2n4c). Stamping a work_dir at route time would bind a
// pool-label path to the bead, which is the gc-j0cfh defect. The claiming session
// is the first actor that knows the concrete tree, and the session bead is the
// only place that records it: GC_WORK_DIR is not in the env of a pool session.
//
// The id is resolved EXACTLY, for the same reason SetCurrentClaim does: the bd
// fuzzy resolver would otherwise let a prefix collision hand back the checkout of
// a different session, and this value is stamped onto the work bead as durable
// execution identity. Every failure yields "" -- an unset gc.work_dir is the
// honest outcome, and the caller stamps nothing rather than guessing a path.
func hookResolveSessionWorkDir(sessionID string) string {
	info, ok := hookLookupSessionInfo(sessionID, "stamping no "+beadmeta.WorkDirMetadataKey)
	if !ok {
		return ""
	}
	return sessionStampableWorkDir(info)
}

// hookResolveSessionSeatDir returns the checkout the session identified by
// sessionID is running in, WITHOUT the pool refusal sessionStampableWorkDir
// applies. It is the production implementation of the
// hookClaimOps.ResolveSessionSeatDir seam.
//
// The two readers ask different questions of the same field. The stamp asks
// "may this path become a bead's ownership evidence?", and a pool slot's
// directory may not, because the next session of that slot reuses it. The seat
// guard (hook_claim_seat.go) asks "is this bead's recorded tree MY tree?", and
// for a pool slot the reused directory is exactly the answer: the next session
// of a seat is the one that should pick up what the last one left there.
func hookResolveSessionSeatDir(sessionID string) string {
	info, ok := hookLookupSessionInfo(sessionID, "guarding no seat")
	if !ok {
		return ""
	}
	return strings.TrimSpace(info.WorkDir)
}

// hookLookupSessionInfo reads the session bead for sessionID through the session
// front door. consequence names what the caller does without it, so the log line
// says which reader lost its answer.
//
// The id is resolved EXACTLY, for the same reason SetCurrentClaim does: the bd
// fuzzy resolver would otherwise let a prefix collision hand back the checkout of
// a different session. Every failure yields ok=false; the caller decides what an
// unknown session means for it.
func hookLookupSessionInfo(sessionID, consequence string) (session.Info, bool) {
	if strings.TrimSpace(sessionID) == "" {
		return session.Info{}, false
	}
	sessFront, err := sessionCurrentClaimFrontDoor()
	if err != nil {
		// Each failure below returns the same answer as a session that simply has
		// no checkout, so without naming the step a lookup fault is
		// indistinguishable in production from the ordinary refusal. The caller
		// behavior is unchanged; only the diagnosis is.
		log.Printf("hook session lookup: opening the session front door for %q: %v; %s",
			sessionID, err, consequence)
		return session.Info{}, false
	}
	resolved, err := sessFront.ResolveIDByExactID(strings.TrimSpace(sessionID))
	if err != nil {
		log.Printf("hook session lookup: resolving session id %q exactly: %v; %s",
			sessionID, err, consequence)
		return session.Info{}, false
	}
	info, err := sessFront.Get(resolved)
	if err != nil {
		log.Printf("hook session lookup: reading session bead %q: %v; %s",
			resolved, err, consequence)
		return session.Info{}, false
	}
	return info, true
}

// sessionStampableWorkDir returns the checkout of info that may be stamped onto a
// work bead as gc.work_dir, or "" when info has none that qualifies. It is the
// decidable half of hookResolveSessionWorkDir, split out so the pool refusal is
// testable without a live city.
//
// A POOL-MANAGED session is refused outright: its WorkDir is the slot label
// (worker-slots/worker-N), a SHARED directory rather than an isolated worktree,
// so stamping it would turn a slot label into worktree-ownership evidence on the
// bead (gc-j0cfh). The reconciler side refuses the same value for the same reason
// (workDirStampHasOwnershipEvidence), and this keeps the claim path from becoming
// a second way to mint it.
//
// The classifier is isPoolManagedSessionInfo, this package's canonical one, not
// the raw PoolManaged flag: a session carrying a pool_slot, or one whose origin is
// ephemeral, is running in a shared slot directory for exactly the same gc-j0cfh
// reason even when pool_managed was never stamped on its bead.
func sessionStampableWorkDir(info session.Info) string {
	if isPoolManagedSessionInfo(info) {
		return ""
	}
	return strings.TrimSpace(info.WorkDir)
}
