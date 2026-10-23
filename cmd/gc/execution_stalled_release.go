package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/storeref"
)

// releaseExecutionStalledClaim gives back, once an execution-stalled drain's
// stop is confirmed, the ONE claim the execution backstop drained the seat for.
// It is the legacy finalize's twin of upstream's stop effect (gascity #7911,
// "a stalled stop gives its claim back"), and it closes the loop that drain
// left open here:
//
//  1. The backstop drains a seat that held a claim without executing it
//     (execution_backstop.go), and the drain scan acknowledges that drain on
//     the controller's own behalf one tick later. The agent never runs
//     `gc runtime drain-ack`, so its held-claim release never runs either.
//  2. finalizeDrainAckStoppedSession then found the seat still assigned, kept
//     it asleep on the claim (sleep_reason=idle) and only raised
//     session.drain_acked_with_assigned_work.
//  3. The awake set woke that asleep row for its assigned work, while the
//     fresh-mode pool path (gastownhall/gascity#4849) refused to resume an
//     asleep row and minted a clean seat for the same bead. The clean seat
//     found the bead held by the re-woken row, drained empty, and the
//     backstop drained the row again about 20 minutes later.
//
// Measured on vessel-network 2026-10-22/23 (vn-ttvpe81): 51 empty seats in 20
// hours across three fresh-mode pools, 20 of them for one squad bead.
//
// Only the claim the backstop's marker names is released, read live off the
// seat's own legs, and only through releaseConfirmedOrphanSessionWork: the
// stranded repair's policy, fenced on the bead still being in_progress and
// assigned to this seat, its route kept. A second claim the seat holds is
// left alone, so the seat stays open for it. Any other drain reason releases
// nothing: a seat drained for idleness that still holds work keeps it, as
// before. Best effort throughout: a claim this cannot release stays held and
// the finalize behaves exactly as it did.
//
// The caller has confirmed the runtime is stopped (every finalize site runs
// after a liveness observation of not-running), which is the precondition
// releaseConfirmedOrphanSessionWork requires: releasing work from a live seat
// is data loss, not recovery.
func releaseExecutionStalledClaim(
	cityPath string,
	cfg *config.City,
	store beads.Store,
	rigStores map[string]beads.Store,
	info sessionpkg.Info,
	now time.Time,
	rec events.Recorder,
	stderr io.Writer,
) bool {
	if cfg == nil || store == nil || info.ID == "" {
		return false
	}
	if strings.TrimSpace(info.DrainReasonMetadata) != executionStalledDrainReason {
		return false
	}
	beadID := strings.TrimSpace(info.ExecutionClaimNudgeWork)
	if beadID == "" {
		return false
	}
	if stderr == nil {
		stderr = io.Discard
	}
	name := strings.TrimSpace(info.SessionNameMetadata)
	plan, err := assignedWorkPlanForSessionInfo(cityPath, cfg, store, rigStores, info)
	if err != nil {
		fmt.Fprintf(stderr, "session reconciler: execution-stalled stop of %s: resolving the legs that may hold claim %s: %v\n", name, beadID, err) //nolint:errcheck
		return false
	}
	var (
		claim beads.Bead
		owner beads.Store
		found bool
	)
	// A Get by id on each leg in plan order: the claim lives in exactly one
	// store, and a leg that does not hold it answers ErrNotFound. Any other
	// read error is logged and the walk goes on; a leg that cannot be read
	// releases nothing from itself, and the next tick's finder reports the
	// strand it left behind (session.drain_acked_with_assigned_work).
	if _, walkErr := storeref.Walk(plan, func(leg storeref.Leg) (bool, error) {
		if leg.Store == nil {
			return false, nil
		}
		b, getErr := leg.Store.Get(beadID)
		if getErr != nil {
			if !errors.Is(getErr, beads.ErrNotFound) {
				fmt.Fprintf(stderr, "session reconciler: execution-stalled stop of %s: reading claim %s: %v\n", name, beadID, getErr) //nolint:errcheck
			}
			return false, nil
		}
		claim, owner, found = b, leg.Store, true
		return true, nil
	}); walkErr != nil {
		fmt.Fprintf(stderr, "session reconciler: execution-stalled stop of %s: walking the legs for claim %s: %v\n", name, beadID, walkErr) //nolint:errcheck
	}
	if !found {
		return false
	}
	released := releaseConfirmedOrphanSessionWork(cfg, store, rigStores, []beads.Bead{claim}, []beads.Store{owner}, info)
	if len(released) == 0 {
		return false
	}
	emitDeadAssigneeReopenedEvents(rec, []beads.Bead{claim}, released, now)
	fmt.Fprintf(stderr, "session reconciler: execution-stalled stop of %s gave back its claim %s\n", name, beadID) //nolint:errcheck
	return true
}
