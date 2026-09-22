package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/storeref"
)

// recordCurrentBeadIDOnWake persists the work bead a session is being woken
// for. The reconciler writes this whenever a session is brought up (asleep
// → awake or alive cycle) so that subsequent reconciler ticks can detect
// when the assignee has been pointed at a different bead. The metadata
// survives session restart, so crash recovery can resume the same bead
// instead of jumping to a sibling assignment.
// recordCurrentBeadIDOnWake returns the metadata patch it applied (the
// currently_processing_bead_id write) so the reconciler can fold it onto the
// infoByID snapshot (write-returns-Info), or nil when it was a no-op. It reads
// the session id and the currently-processing bead off the caller's coherent
// typed Info (Info.ID / Info.CurrentlyProcessingBeadID, both verbatim raw
// mirrors); the fold the caller applies keeps the snapshot in step.
func recordCurrentBeadIDOnWake(info sessionpkg.Info, sessFront *sessionpkg.Store, beadID string, stderr io.Writer) sessionpkg.MetadataPatch {
	if strings.TrimSpace(info.ID) == "" || sessFront == nil {
		return nil
	}
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return nil
	}
	if info.CurrentlyProcessingBeadID == beadID {
		return nil
	}
	if err := sessFront.RecordCurrentBead(info.ID, beadID); err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "session reconciler: recording %s for %s: %v\n", sessionpkg.CurrentBeadIDKey, info.SessionNameMetadata, err) //nolint:errcheck
		}
		return nil
	}
	return sessionpkg.MetadataPatch{sessionpkg.CurrentBeadIDKey: beadID}
}

// freshCycleOutcome says what cycleAliveSessionForFreshReassign did.
type freshCycleOutcome int

const (
	// freshCycleFailed: a write or the kill failed. Nothing was proven either
	// way, so the caller carries on as before.
	freshCycleFailed freshCycleOutcome = iota
	// freshCycleRefused: the session's own bead has not provably left it, so
	// this is not a reassignment. The session was NOT touched, and the caller
	// must not re-stamp currently_processing_bead_id with the fallback bead
	// either: that would make the record name a bead the session is not on.
	freshCycleRefused
	// freshCycleRan: the session was killed and primed for a fresh wake.
	freshCycleRan
)

// freshReassignGate is the memory the fresh-mode reassign check keeps between
// reconciler ticks. It lives on the CityRuntime, so it lasts as long as the
// controller does. A nil gate is safe: it logs every refusal and never
// confirms a missing bead, so a caller without one can refuse a kill but
// never cause one on a missing read.
type freshReassignGate struct {
	mu sync.Mutex
	// missing maps session name to the recorded bead the last tick could not
	// find in any store. A second miss in a row confirms the bead is gone.
	missing map[string]string
	// refused maps session name to the refusal last logged, so each refusal
	// is logged once and not once per tick.
	refused map[string]string
}

func newFreshReassignGate() *freshReassignGate {
	return &freshReassignGate{missing: map[string]string{}, refused: map[string]string{}}
}

// missedAgain records that beadID was not found for this session and reports
// whether the tick before this one also missed it.
func (g *freshReassignGate) missedAgain(name, beadID string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.missing[name] == beadID {
		return true
	}
	g.missing[name] = beadID
	return false
}

// found clears a recorded miss, so two misses must be back to back.
func (g *freshReassignGate) found(name string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.missing, name)
}

// firstRefusal reports whether this refusal has not been logged yet.
func (g *freshReassignGate) firstRefusal(name, key string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refused[name] == key {
		return false
	}
	g.refused[name] = key
	return true
}

// forget drops everything the gate knows about a session. Call it when the
// session is cycled or its anchor matches its record again.
func (g *freshReassignGate) forget(name string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.missing, name)
	delete(g.refused, name)
}

// proveFreshReassign decides whether a live session's recorded bead
// (currently_processing_bead_id) has really left it. It reads that bead
// straight from the stores the session can reach. It does not trust the
// tick's work list.
//
// Why: the list is a snapshot of ready and in-progress work. A bead can be
// missing from it because it is open but not ready, because the list is
// bounded, or because one read failed. On 2026-09-11 a missing entry was read
// as "reassigned" and killed a live refinery mid-turn, twice in 3 minutes,
// each time picking another bead that was ALREADY assigned to the same
// session (vn-9y7tkv1). Making the list bigger only moves that edge.
//
// Proof is one of:
//   - the bead is closed,
//   - the bead is assigned to someone else, or to nobody,
//   - the bead is found in no store, on two ticks in a row. A burned wisp is
//     deleted, not closed, so this is its only proof. One miss is not
//     enough, because a wisp read that fails comes back as "not found".
//
// A failed read proves nothing. The answer is then "not proven".
func proveFreshReassign(
	cityPath string,
	cfg *config.City,
	store beads.Store,
	rigStores map[string]beads.Store,
	info sessionpkg.Info,
	name string,
	gate *freshReassignGate,
) (bool, string) {
	prev := strings.TrimSpace(info.CurrentlyProcessingBeadID)
	if prev == "" {
		return false, "no recorded bead to compare against"
	}
	plan, err := assignedWorkPlanForSessionInfo(cityPath, cfg, store, rigStores, info)
	if err != nil {
		return false, fmt.Sprintf("could not plan a read of %s: %v", prev, err)
	}
	var (
		bead  beads.Bead
		found bool
	)
	res, err := storeref.Walk(plan, func(leg storeref.Leg) (bool, error) {
		if leg.Store == nil {
			return false, nil
		}
		b, getErr := leg.Store.Get(prev)
		if getErr != nil {
			if errors.Is(getErr, beads.ErrNotFound) {
				return false, nil
			}
			return false, getErr
		}
		bead, found = b, true
		return true, nil
	})
	if err != nil {
		return false, fmt.Sprintf("could not read %s: %v", prev, err)
	}
	if !found {
		if err := assignedWorkScanComplete(res); err != nil {
			return false, fmt.Sprintf("could not read %s: %v", prev, err)
		}
		if gate.missedAgain(name, prev) {
			return true, fmt.Sprintf("%s was found in no store on two ticks in a row", prev)
		}
		return false, fmt.Sprintf("%s was found in no store on this tick; one miss is not proof, waiting for the next tick", prev)
	}
	gate.found(name)
	if bead.Status == "closed" {
		return true, fmt.Sprintf("%s is closed", prev)
	}
	assignee := strings.TrimSpace(bead.Assignee)
	for _, id := range sessionAssignmentIdentifiersForConfigInfo(info, cfg) {
		if id != "" && id == assignee {
			return false, fmt.Sprintf("%s is still %s and still assigned to this session; it was only missing from this tick's work list", prev, bead.Status)
		}
	}
	if assignee == "" {
		return true, fmt.Sprintf("%s is no longer assigned to anyone", prev)
	}
	return true, fmt.Sprintf("%s is now assigned to %s", prev, assignee)
}

// cycleAliveSessionForFreshReassign tears down a live wake_mode=fresh
// session whose assigned bead has changed, then primes the bead so the
// next reconciler tick wakes the session on a brand-new conversation.
// It returns freshCycleRan when the cycle ran; the caller must `continue` so
// it does not double-process the drain/idle bookkeeping for a session it just
// killed. The fold is the in-memory mirror it applied (RestartRequestPatch
// minus ResetCommittedAtKey), for the reconciler to fold onto the infoByID
// snapshot (write-returns-Info).
//
// It kills nothing until proveFreshReassign says the session's recorded bead
// has really left it. A different anchor on one tick is only a suspicion.
// When the proof fails it refuses, logs why once per refusal, and returns
// freshCycleRefused.
//
// The teardown path mirrors the agent-initiated restart handoff
// (`gc runtime request-restart`): kill the process, reset the named-session
// circuit breaker (a cycle is deliberate, not a crash — accumulated breaker
// state must not block the post-cycle wake), optionally rotate session_key
// for providers that accept --session-id, then apply RestartRequestPatch so
// the next wake observes firstStart=true and uses the fresh-wake
// conversation reset. We also update currently_processing_bead_id to the
// new anchor so the divergence check does not refire on the next tick.
func cycleAliveSessionForFreshReassign(
	cityPath string,
	info sessionpkg.Info,
	tp TemplateParams,
	sp runtime.Provider,
	store beads.Store,
	rigStores map[string]beads.Store,
	cfg *config.City,
	cb *sessionCircuitBreaker,
	gate *freshReassignGate,
	name string,
	newBeadID string,
	now time.Time,
	stdout, stderr io.Writer,
	trace *sessionReconcilerTraceCycle,
) (freshCycleOutcome, sessionpkg.MetadataPatch) {
	if store == nil {
		return freshCycleFailed, nil
	}
	newBeadID = strings.TrimSpace(newBeadID)
	if newBeadID == "" {
		return freshCycleFailed, nil
	}
	prevBeadID := strings.TrimSpace(info.CurrentlyProcessingBeadID)
	proven, why := proveFreshReassign(cityPath, cfg, store, rigStores, info, name, gate)
	if !proven {
		if gate.firstRefusal(name, prevBeadID+" -> "+newBeadID+": "+why) && stderr != nil {
			fmt.Fprintf(stderr, "session reconciler: not cycling fresh-mode session '%s' for bead reassign %s -> %s: %s\n", name, prevBeadID, newBeadID, why) //nolint:errcheck
		}
		if trace != nil {
			trace.RecordDecision(TraceSiteReconcilerBeadReassignCycle, TraceReasonFreshCycleUnproven, TraceOutcomeSkipped, tp.TemplateName, name, traceRecordPayload{
				"previous_bead_id": prevBeadID,
				"new_bead_id":      newBeadID,
				"why":              why,
			})
		}
		return freshCycleRefused, nil
	}
	if err := workerKillSessionTargetWithConfig("", store, sp, cfg, name); err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "session reconciler: stopping fresh-cycle %s: %v\n", name, err) //nolint:errcheck
		}
		return freshCycleFailed, nil
	}
	if identity := namedSessionIdentityInfo(info); identity != "" {
		if err := resetSessionCircuitBreakerState(store, info.ID, identity, cb); err != nil {
			if stderr != nil {
				fmt.Fprintf(stderr, "session reconciler: clearing session circuit breaker for fresh-cycle %s: %v\n", name, err) //nolint:errcheck
			}
			return freshCycleFailed, nil
		}
	}
	newSessionKey, hasCapability := freshRestartSessionKeyInfo(tp, info)
	batch := sessionpkg.RestartRequestPatch(newSessionKey, now)
	if hasCapability && newSessionKey == "" {
		batch["session_key"] = ""
	}
	batch[sessionpkg.CurrentBeadIDKey] = newBeadID
	if err := sessionFrontDoor(store).ApplyPatch(info.ID, batch); err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "session reconciler: recording fresh-cycle handoff for %s: %v\n", name, err) //nolint:errcheck
		}
		return freshCycleFailed, nil
	}
	// The returned fold carries every batch key EXCEPT the durable reset commit
	// marker: keeping ResetCommittedAtKey out of this tick's snapshot mirrors the
	// restart-requested handoff so on-demand sessions are not force-woken without
	// demand within the same tick. The former raw session.Metadata mirror loop is
	// deleted — it wrote the identical key set as this fold, and the caller applies
	// the fold to infoByID before `continue`ing (no later raw read this tick).
	fold := make(sessionpkg.MetadataPatch, len(batch))
	for key, value := range batch {
		if key == sessionpkg.ResetCommittedAtKey {
			continue
		}
		fold[key] = value
	}
	if stdout != nil {
		fmt.Fprintf(stdout, "Cycled fresh-mode session '%s' for bead reassign: %s → %s\n", name, prevBeadID, newBeadID) //nolint:errcheck
	}
	if trace != nil {
		trace.RecordDecision(TraceSiteReconcilerBeadReassignCycle, TraceReasonFreshCycle, TraceOutcomeRestart, tp.TemplateName, name, traceRecordPayload{
			"previous_bead_id": prevBeadID,
			"new_bead_id":      newBeadID,
		})
	}
	gate.forget(name)
	return freshCycleRan, fold
}
