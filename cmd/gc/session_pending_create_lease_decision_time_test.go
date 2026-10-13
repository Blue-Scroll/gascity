package main

import (
	"strings"
	"testing"
	"time"
)

// TestReconciler_JudgesNeverStartedLeaseAtTickDecisionTime pins the fix for
// vn-m0i7sn9. One tick ran 41 minutes, allocated pool beads 28 minutes in,
// reached them in its forward pass 12 minutes after that, read their
// never-started lease (10m) as expired on the live clock and rolled them back
// before its own start phase had run. The pass must judge the lease at the
// tick's start: a bead the tick allocated is younger than the tick, so its
// lease is fresh however long the pass takes to reach it.
func TestReconciler_JudgesNeverStartedLeaseAtTickDecisionTime(t *testing.T) {
	h := newSessionChaosHarness(t, 20261012)
	h.createSessionIntent()
	h.assertCreatingIntent()

	// The slow tick began here, and allocated this bead 28 minutes in.
	tickStart := h.env.clk.Now().UTC()
	h.env.clk.Advance(28 * time.Minute)
	markConfiguredNamedPendingCreate(t, h, map[string]string{
		"pending_create_started_at": h.env.clk.Now().UTC().Format(time.RFC3339),
	})
	h.setDesired(false)
	// The forward pass reaches the bead 12 minutes later: past the lease on
	// the live clock, 16 minutes before it on the tick's clock.
	h.env.clk.Advance(12 * time.Minute)

	liveOptions := h.env.startOptions
	h.env.startOptions = append(append([]startExecutionOption(nil), liveOptions...), withLeaseDecisionTime(tickStart))
	h.reconcileTick()

	got, err := h.env.store.Get(h.sessionID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", h.sessionID, err)
	}
	if got.Status == "closed" {
		t.Fatalf("status = closed: the tick rolled back a create it allocated itself (state=%q)", got.Metadata["state"])
	}
	if strings.TrimSpace(got.Metadata["pending_create_claim"]) != "true" {
		t.Fatalf("pending_create_claim = %q, want true: lease judged at the tick's start is still fresh", got.Metadata["pending_create_claim"])
	}

	// Control: the same bead judged on the live clock is rolled back, so the
	// assertions above can fail.
	h.env.startOptions = liveOptions
	h.reconcileTick()
	got, err = h.env.store.Get(h.sessionID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", h.sessionID, err)
	}
	if got.Status != "closed" || got.Metadata["state"] != "failed-create" {
		t.Fatalf("live-clock control: status=%q state=%q, want closed failed-create", got.Status, got.Metadata["state"])
	}
}
