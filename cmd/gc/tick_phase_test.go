package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestTickPhaseWithBDCost proves a phase record carries the bd calls made
// since the phase began, keeps the caller's fields, and leaves the caller's
// map untouched.
func TestTickPhaseWithBDCost(t *testing.T) {
	t.Setenv("GC_BD_TRACE_JSON", "")
	start := startTickPhase()
	for range 2 {
		beads.TraceBDCall("go:test", t.TempDir(), []string{"list"}, time.Now().Add(-5*time.Millisecond), 0, nil)
	}
	fields := map[string]any{"finalized": 2}
	got := start.withBDCost(fields)

	if calls, _ := got["bd_calls"].(int64); calls < 2 {
		t.Fatalf("bd_calls = %v, want at least 2", got["bd_calls"])
	}
	if ms, _ := got["bd_ms"].(int64); ms < 10 {
		t.Fatalf("bd_ms = %v, want at least 10", got["bd_ms"])
	}
	if got["finalized"] != 2 {
		t.Fatalf("caller field lost: %#v", got)
	}
	if len(fields) != 1 {
		t.Fatalf("caller map was changed: %#v", fields)
	}
}
