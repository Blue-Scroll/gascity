package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

// TestReapStaleSessionBeads_SkipsSessionWhoseStartIsStillInFlight pins hq-dx354v.
//
// The reaper used to decide "this start is never coming" from a clock alone. On
// a slow disk a provider start really does take longer than the grace window,
// so the reaper closed the bead while the start goroutine was still running.
// When the session finally came up, the commit found its bead closed, threw the
// result away as stale_async_start, and STOPPED the live session. Measured
// 2026-09-20: no polecat start succeeded for 20 minutes, and six claimed work
// beads were freed by the closes.
//
// A start that is still running in this process is not stale, whatever the
// clock says. Once the goroutine ends the old windows apply again, so a bead
// whose start really did die is still reaped (the gc-5tyf5 phantom leak).
func TestReapStaleSessionBeads_SkipsSessionWhoseStartIsStillInFlight(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake() // start has not finished, so no runtime is running
	created, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name": "worker-1",
			"state":        "creating",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// preWakeCommit stamps last_woke_at and state=creating before the provider
	// start runs. Put it far past every grace window in the reaper so only the
	// in-flight guard can save this bead.
	woke := created.CreatedAt.Add(time.Minute)
	if err := store.SetMetadata(created.ID, "last_woke_at", woke.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("SetMetadata(last_woke_at): %v", err)
	}
	now := woke.Add(30 * time.Minute)
	clk := &clock.Fake{Time: now}

	starts := &asyncStartTracker{}
	done, ok := starts.beginStart(created.ID)
	if !ok {
		t.Fatalf("beginStart returned not-ok on a fresh tracker")
	}

	var stderr bytes.Buffer
	if got := reapStaleSessionBeads(store, sp, nil, starts, clk, &stderr); got != 0 {
		t.Fatalf("reapStaleSessionBeads() = %d, want 0 while the start goroutine is still running\nstderr: %s", got, stderr.String())
	}
	open, err := loadSessionBeads(store)
	if err != nil {
		t.Fatalf("loadSessionBeads: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open session beads = %d, want 1 (the bead must survive its own start)", len(open))
	}

	// The start ended without committing: now the bead really is a phantom and
	// the existing windows must still reap it.
	done()
	stderr.Reset()
	if got := reapStaleSessionBeads(store, sp, nil, starts, clk, &stderr); got != 1 {
		t.Fatalf("reapStaleSessionBeads() = %d, want 1 once the start goroutine has ended\nstderr: %s", got, stderr.String())
	}
}

// TestAsyncStart_MarksSessionInFlightSoTheReaperSparesIt is the wiring half of
// hq-dx354v: the async start path must record the session bead it is starting,
// or the reaper's guard above has nothing to read.
func TestAsyncStart_MarksSessionInFlightSoTheReaperSparesIt(t *testing.T) {
	store := beads.NewMemStore()
	clk := &clock.Fake{Time: time.Date(2026, 9, 20, 23, 55, 0, 0, time.UTC)}
	session, err := store.Create(beads.Bead{
		ID:     "gc-worker",
		Title:  "worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name":       "worker",
			"template":           "worker",
			"state":              "asleep",
			"sleep_reason":       "idle",
			"wake_mode":          "fresh",
			"generation":         "1",
			"continuation_epoch": "1",
			"instance_token":     "tok-worker",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sp := newGatedStartProvider()
	tp := TemplateParams{Command: "worker", SessionName: "worker", TemplateName: "worker"}
	cfg := &config.City{Agents: []config.Agent{{Name: "worker"}}}
	starts := &asyncStartTracker{}

	executePlannedStartsTraced(
		context.Background(),
		[]startCandidate{{info: sessiontest.SeedBead(t, session), tp: tp}},
		cfg,
		map[string]TemplateParams{"worker": tp},
		sp,
		store,
		"test-city",
		"",
		clk,
		events.Discard,
		time.Minute,
		ioDiscard{},
		ioDiscard{},
		nil,
		withAsyncStartExecution(),
		withAsyncStartTracker(starts),
	)

	sp.waitForStarts(t, 1)

	if !starts.startInFlight(session.ID) {
		t.Fatalf("startInFlight(%q) = false while the provider is inside Start", session.ID)
	}
	// The whole point: a reaper tick landing here must leave the bead alone,
	// even with a clock far past every grace window.
	var stderr bytes.Buffer
	reapClk := &clock.Fake{Time: clk.Now().Add(30 * time.Minute)}
	if got := reapStaleSessionBeads(store, sp, nil, starts, reapClk, &stderr); got != 0 {
		t.Fatalf("reapStaleSessionBeads() = %d, want 0 during a live start\nstderr: %s", got, stderr.String())
	}

	sp.release("worker")
	if !starts.wait(hangBudget) {
		t.Fatalf("async start goroutine did not finish")
	}
	if starts.startInFlight(session.ID) {
		t.Fatalf("startInFlight(%q) = true after the start goroutine ended", session.ID)
	}
	got, err := store.Get(session.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status == "closed" {
		t.Fatalf("session bead was closed even though its start succeeded")
	}
}
