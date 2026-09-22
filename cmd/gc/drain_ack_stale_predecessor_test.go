package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// These tests pin vn-ard8cvj (2026-09-22). preWakeCommit wrote generation 4
// and a new token onto a pool session bead, but the start never replaced the
// box, which kept running generation 3 with the old token. The drain-ack stop
// then read "token differs" as "the session was replaced" and skipped on every
// tick for 4 hours. The box kept its name, the pool could not reuse it, and
// because the pool's demand was exactly 1 it started nothing at all.

// startBox starts a fake runtime under name and stamps the identity a real
// managed start gives it.
func startBox(t *testing.T, sp *runtime.Fake, name, sessionID, generation, token string) {
	t.Helper()
	if err := sp.Start(context.Background(), name, runtime.Config{Command: "test-cmd"}); err != nil {
		t.Fatalf("Start(%s): %v", name, err)
	}
	for k, v := range map[string]string{
		"GC_SESSION_ID":     sessionID,
		"GC_RUNTIME_EPOCH":  generation,
		"GC_INSTANCE_TOKEN": token,
	} {
		if v == "" {
			continue
		}
		if err := sp.SetMeta(name, k, v); err != nil {
			t.Fatalf("SetMeta(%s, %s): %v", name, k, err)
		}
	}
}

func TestDrainAckStopFence(t *testing.T) {
	target := drainAckStopTarget{SessionID: "gc-1", Name: "worker", Token: "gen4-token", Generation: "4"}
	cases := []struct {
		name    string
		box     [3]string // GC_SESSION_ID, GC_RUNTIME_EPOCH, GC_INSTANCE_TOKEN
		target  drainAckStopTarget
		verdict drainAckStopFenceVerdict
	}{
		{"matching token is the target", [3]string{"gc-1", "4", "gen4-token"}, target, drainAckStopKill},
		{"no expected token cannot be verified, so kill", [3]string{"gc-1", "3", "gen3-token"}, drainAckStopTarget{SessionID: "gc-1", Name: "worker", Generation: "4"}, drainAckStopKill},
		{"no live token cannot be verified, so kill", [3]string{"gc-1", "3", ""}, target, drainAckStopKill},
		{"older generation of the same session is the stale box", [3]string{"gc-1", "3", "gen3-token"}, target, drainAckStopKillStalePredecessor},
		{"newer generation of the same session is a real replacement", [3]string{"gc-1", "5", "gen5-token"}, target, drainAckStopSkipNotOurs},
		{"same generation with another token is not provably older", [3]string{"gc-1", "4", "other-token"}, target, drainAckStopSkipNotOurs},
		{"another session holds the name", [3]string{"gc-2", "1", "gc2-token"}, target, drainAckStopSkipNotOurs},
		{"box with no session id is not provably ours", [3]string{"", "3", "gen3-token"}, target, drainAckStopSkipNotOurs},
		{"box with no generation is not provably older", [3]string{"gc-1", "", "gen3-token"}, target, drainAckStopSkipNotOurs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := runtime.NewFake()
			startBox(t, sp, "worker", tc.box[0], tc.box[1], tc.box[2])
			got, why := drainAckStopFence(sp, tc.target)
			if got != tc.verdict {
				t.Fatalf("verdict = %d (%q), want %d", got, why, tc.verdict)
			}
			if got != drainAckStopKill && why == "" {
				t.Fatal("a mismatch verdict must say why in words")
			}
		})
	}
}

func TestQueueDrainAckAsyncStopKillsStalePredecessorWhoseReplacementNeverStarted(t *testing.T) {
	old := drainAckAsyncStopPokeController
	drainAckAsyncStopPokeController = func(string) error { return nil }
	t.Cleanup(func() { drainAckAsyncStopPokeController = old })

	oldTimeout, oldPoll := drainAckStopConfirmDeadTimeout, drainAckStopConfirmDeadPoll
	drainAckStopConfirmDeadTimeout = 300 * time.Millisecond
	drainAckStopConfirmDeadPoll = 20 * time.Millisecond
	t.Cleanup(func() {
		drainAckStopConfirmDeadTimeout = oldTimeout
		drainAckStopConfirmDeadPoll = oldPoll
	})

	// A real bead: the kill resolves the box's GC_SESSION_ID to it, as in
	// production.
	store := beads.NewMemStore()
	bead := seedPoolSessionBead(t, store, poolChurnIdentity(), map[string]string{
		"generation":     "4",
		"instance_token": "gen4-token",
		"state":          string(sessionpkg.StateDraining),
	})
	target := drainAckStopTarget{SessionID: bead.ID, Name: strings.TrimSpace(bead.SessionNameMetadata), Token: "gen4-token", Generation: "4"}
	sp := runtime.NewFake()
	startBox(t, sp, target.Name, bead.ID, "3", "gen3-token")

	var stderr synchronizedBuffer
	tracker := &asyncStartTracker{}
	queueDrainAckAsyncStop("", store, sp, &config.City{}, target, nil, tracker, &stderr)
	if !tracker.wait(2 * time.Second) {
		t.Fatal("async drain-ack stop did not complete")
	}
	if sp.IsRunning(target.Name) {
		t.Fatalf("the generation-3 box outlived a stop aimed at generation 4 of the same session; stderr=%q", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "replacement never started") {
		t.Fatalf("stderr = %q, want the stale-box reason", got)
	}
}

func TestQueueDrainAckAsyncStopReportsASkipOnceNotEveryTick(t *testing.T) {
	sp := runtime.NewFake()
	// A real replacement: newer generation of the same session.
	startBox(t, sp, "worker", "gc-1", "5", "gen5-token")

	var stderr synchronizedBuffer
	tracker := &asyncStartTracker{}
	target := drainAckStopTarget{SessionID: "gc-1", Name: "worker", Token: "gen4-token", Generation: "4"}
	for tick := 0; tick < 3; tick++ {
		queueDrainAckAsyncStop("", beads.NewMemStore(), sp, &config.City{}, target, nil, tracker, &stderr)
		if !tracker.wait(time.Second) {
			t.Fatalf("tick %d: async drain-ack stop did not complete", tick)
		}
	}
	if !sp.IsRunning("worker") {
		t.Fatal("the fence killed a newer-generation replacement")
	}
	got := stderr.String()
	if n := strings.Count(got, "skipped"); n != 1 {
		t.Fatalf("skip printed %d times over 3 ticks, want 1; stderr=%q", n, got)
	}
	if !strings.Contains(got, "generation 5") {
		t.Fatalf("stderr = %q, want the skip to say why (the box's generation)", got)
	}
}

// TestDrainAckStalePredecessorDemandOnePoolStartsASeatOnTheNextTick walks the
// measured outage end to end for a pool whose demand is exactly 1. With 2 or
// more seats the other slots still start, which is why this went unseen: here
// the one slot IS the stuck name, so the pool starts nothing until the stale
// box is stopped and its bead is closed.
func TestDrainAckStalePredecessorDemandOnePoolStartsASeatOnTheNextTick(t *testing.T) {
	old := drainAckAsyncStopPokeController
	drainAckAsyncStopPokeController = func(string) error { return nil }
	t.Cleanup(func() { drainAckAsyncStopPokeController = old })

	store := beads.NewMemStore()
	sp := runtime.NewFake()
	identity := poolChurnIdentity() // the one seat demand 1 asks for
	cfg := &config.City{}

	// The measured bead: pre-woken to generation 4 with a new token, then
	// drained to drain-ack-stop-pending.
	stuck := seedPoolSessionBead(t, store, identity, map[string]string{
		"generation":     "4",
		"instance_token": "gen4-token",
	})
	if err := store.SetMetadataBatch(stuck.ID, sessionpkg.DrainAckStopPendingPatch(time.Now().UTC())); err != nil {
		t.Fatalf("marking stop-pending: %v", err)
	}
	name := strings.TrimSpace(stuck.SessionNameMetadata)
	// The box never moved past generation 3.
	startBox(t, sp, name, stuck.ID, "3", "gen3-token")

	openInfos := func() []sessionpkg.Info {
		open, err := loadSessionBeads(store)
		if err != nil {
			t.Fatalf("loadSessionBeads: %v", err)
		}
		return newSessionBeadSnapshot(open).OpenInfos()
	}
	createSeat := func() (sessionpkg.Info, error) {
		open, err := loadSessionBeads(store)
		if err != nil {
			t.Fatalf("loadSessionBeads: %v", err)
		}
		return createPoolSessionBeadWithAlias(store, poolChurnTemplate, nil, newSessionBeadSnapshot(open), time.Now().UTC(), identity, "")
	}

	// The outage: the only seat is refused because the stuck bead holds its name.
	if _, err := createSeat(); !errors.Is(err, errPoolSessionNameUnavailable) {
		t.Fatalf("precondition: create error = %v, want errPoolSessionNameUnavailable", err)
	}

	var stderr synchronizedBuffer
	tracker := &asyncStartTracker{}
	finalize := func() int {
		return finalizeDrainAckStopPendingSessions(
			"", cfg, sp, beads.SessionStore{Store: store}, nil, openInfos(),
			newFakeDrainOps(), newDrainTracker(), tracker, &clock.Fake{Time: time.Now()}, events.Discard, &stderr,
		)
	}

	// Tick 1: the box is alive, so the finalizer queues the stop.
	if got := finalize(); got != 0 {
		t.Fatalf("tick 1 finalized %d, want 0 while the box is alive", got)
	}
	if !tracker.wait(2 * time.Second) {
		t.Fatal("async drain-ack stop did not complete")
	}
	if sp.IsRunning(name) {
		t.Fatalf("tick 1 left the stale generation-3 box running; stderr=%q", stderr.String())
	}

	// Tick 2: the box is gone, so the finalizer closes the bead and frees the name.
	if got := finalize(); got != 1 {
		t.Fatalf("tick 2 finalized %d, want 1; stderr=%q", got, stderr.String())
	}
	seat, err := createSeat()
	if err != nil {
		t.Fatalf("the demand-1 pool still cannot start its seat after the stale box was stopped: %v", err)
	}
	if got := strings.TrimSpace(seat.SessionNameMetadata); got != name {
		t.Fatalf("new seat session_name = %q, want the slot's own name %q back (a second name leaks a seat)", got, name)
	}
}
