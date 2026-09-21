package main

// Demand/claim divergence diagnostics.
//
// The controller spawns a seat when it counts demand; the worker finds its own
// work. When those two reads disagree, the symptom is a seat that starts, reads
// empty, and drains — and from the outside that is indistinguishable from the
// HEALTHY case, where a sibling seat simply claimed the row first. One is the
// invariant breaking; the other is pull working exactly as designed.
//
// This file tells them apart, after the fact and only after the fact:
//
//   - It runs AFTER the drain result is written. The drain is not waiting on it,
//     cannot be changed by it, and is byte-identical whether it runs or not.
//   - It reads the trigger bead by id SOLELY to classify. That id is demand
//     bookkeeping, never an assignment: nothing on the claim path may consult it
//     to decide what to claim, and nothing here feeds back into that path.
//   - It is scoped to seats the controller spawned on demand evidence
//     (GC_SPAWN_ORIGIN=demand, presence only). A named session or a manual hook
//     draining empty is not evidence about anything.
//
// The divergence count is the rollout metric for the agreement fix: it should be
// zero. The benign count is expected to be nonzero forever, and a change that
// tried to drive IT to zero would be pathologizing correct pull.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// demandSpawnOriginValue is the presence-only marker the controller sets on a
// seat it minted from counted demand (build_desired_state.go).
const demandSpawnOriginValue = "demand"

// hookRecordDemandClaimDivergence is the emitter seam, replaced in tests.
var hookRecordDemandClaimDivergence = recordDemandClaimDivergence

// demandDivergenceRecorder opens the recorder this diagnostics event is written
// to. A var scoped to this emitter, so a test can read the event back without
// making the process-wide recorder constructor swappable.
var demandDivergenceRecorder = openCityRecorder

// recordDemandClaimDivergence publishes the diagnostics event for a
// demand-spawned seat that drained with no work. It is best-effort and silent on
// every failure: a diagnostics counter must never turn into a second failure
// mode on the drain path.
func recordDemandClaimDivergence(reason, dir string, opts hookClaimOptions, ops hookClaimOps, stderr io.Writer) {
	if hookClaimEnvValue(opts.Env, "GC_SPAWN_ORIGIN") != demandSpawnOriginValue {
		return
	}
	sessionID := hookClaimSessionID(opts.Env)
	if sessionID == "" {
		return
	}
	triggerID := hookClaimEnvValue(opts.Env, "GC_TRIGGER_WORK_BEAD_ID")
	status, classification := classifyDemandTrigger(triggerID, dir, opts, ops)
	payload, err := json.Marshal(events.SessionDemandClaimDivergencePayload{
		SessionID:            sessionID,
		Template:             hookClaimEnvValue(opts.Env, "GC_TEMPLATE"),
		DrainReason:          reason,
		TriggerBeadID:        triggerID,
		TriggerStatusAtDrain: status,
		Classification:       classification,
	})
	if err != nil {
		return
	}
	rec := demandDivergenceRecorder(io.Discard)
	if closer, ok := rec.(io.Closer); ok {
		defer closer.Close() //nolint:errcheck // diagnostics are best-effort
	}
	rec.Record(events.Event{
		Type:      events.SessionDemandClaimDivergence,
		Actor:     eventActor(),
		Subject:   triggerID,
		SessionID: sessionID,
		Message:   classification,
		Payload:   payload,
	})
	if classification == events.DemandClaimDivergence && stderr != nil {
		// The one classification worth a line on the worker's own stderr: the
		// row the controller counted is still sitting there, claimable, and this
		// seat could not see it.
		fmt.Fprintf(stderr, "gc hook --claim: demand/claim divergence: %s is still claimable but this seat's query did not serve it\n", triggerID) //nolint:errcheck
	}
}

// classifyDemandTrigger reads the trigger row and reports (status, verdict).
//
// A read that FAILS is never absence here either: it classifies as unknown
// rather than inventing a divergence, so a flaky store cannot manufacture the
// exact metric this counter exists to trust.
func classifyDemandTrigger(triggerID, dir string, opts hookClaimOptions, ops hookClaimOps) (status, classification string) {
	if strings.TrimSpace(triggerID) == "" {
		return "", events.DemandClaimUnknown
	}
	if ops.ReadWorkMeta == nil {
		return "", events.DemandClaimUnknown
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookClaimMutationTimeout)
	defer cancel()
	bead, err := ops.ReadWorkMeta(ctx, dir, opts.Env, triggerID, opts.Assignee)
	if err != nil {
		return "unreadable", events.DemandClaimUnknown
	}
	status = strings.ToLower(strings.TrimSpace(bead.Status))
	// The invariant is about a row that is STILL claimable by a worker for this
	// template: open, unassigned, route-matching, not excluded by the shared
	// serving rules, and not blocked. Anything else means the row moved on —
	// which is what a sibling claim looks like, and is correct pull.
	if status != "open" || !demandRowServable(bead) || !hookClaimMatchesRoute(bead, opts.RouteTargets) {
		return status, events.DemandClaimBenign
	}
	// The worker's query is built on `bd ready`, which never serves a row with
	// an open blocker. The controller counted this row from Ready(), so it was
	// unblocked then; a blocker added since is the row moving on, not the two
	// readers disagreeing. Without this check every such drain printed a false
	// "still claimable" line (vn-w4huxh6). demandRowServable cannot carry it:
	// the demand loop runs that predicate on Ready() rows, which are already
	// dependency-filtered, and it must stay a pure read of one row.
	blocked, err := demandTriggerBlocked(ctx, bead, func(id string) (beads.Bead, error) {
		return ops.ReadWorkMeta(ctx, dir, opts.Env, id, opts.Assignee)
	})
	if err != nil {
		return status, events.DemandClaimUnknown
	}
	if blocked {
		return status, events.DemandClaimBenign
	}
	return status, events.DemandClaimDivergence
}

// demandTriggerBlocked reports whether bd ready would hold this row back for an
// open blocker. It uses the same rule as the cache's ready read
// (beads.cachedBeadReady): bd's own is_blocked answer wins when the store sent
// one; otherwise any ready-blocking edge whose target is not closed blocks.
//
// A by-id read keeps the edges but drops each blocker's status, so each blocker
// is read here. A blocker read that fails returns the error, never "unblocked":
// guessing either way would let a flaky store fake the metric.
func demandTriggerBlocked(ctx context.Context, bead beads.Bead, read func(id string) (beads.Bead, error)) (bool, error) {
	if bead.IsBlocked != nil {
		return *bead.IsBlocked, nil
	}
	for _, dep := range bead.Dependencies {
		if !beads.IsReadyBlockingDependencyType(dep.Type) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		blocker, err := read(dep.DependsOnID)
		if err != nil {
			return false, err
		}
		if strings.ToLower(strings.TrimSpace(blocker.Status)) != "closed" {
			return true, nil
		}
	}
	return false, nil
}

// demandDivergenceOpsForBead is a tiny adapter so the classification read can be
// driven directly in tests without constructing a whole claim. A read for the id
// of one of blockers returns that blocker; every other read returns bead, err.
func demandDivergenceOpsForBead(bead beads.Bead, err error, blockers ...beads.Bead) hookClaimOps {
	return hookClaimOps{
		ReadWorkMeta: func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, error) {
			for _, blocker := range blockers {
				if blocker.ID == id {
					return blocker, nil
				}
			}
			return bead, err
		},
	}
}
