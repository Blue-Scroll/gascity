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

// TestTickPhasePartsTimesEachPart proves a named part records its own time and
// its own bd calls, under keys that cannot collide with the phase's bd_calls
// and bd_ms, and that RecordTickPhase's copy keeps every part.
func TestTickPhasePartsTimesEachPart(t *testing.T) {
	t.Setenv("GC_BD_TRACE_JSON", "")
	phase := startTickPhase()
	parts := tickPhaseParts{}
	parts.time("quiet", func() {})
	parts.time("busy", func() {
		for range 3 {
			beads.TraceBDCall("go:test", t.TempDir(), []string{"list"}, time.Now(), 0, nil)
		}
	})

	if calls, _ := parts["busy_bd_calls"].(int64); calls < 3 {
		t.Fatalf("busy_bd_calls = %v, want at least 3", parts["busy_bd_calls"])
	}
	if _, ok := parts["quiet_ms"].(int64); !ok {
		t.Fatalf("quiet_ms missing or not int64: %#v", parts)
	}
	if _, ok := parts["quiet_bd_calls"].(int64); !ok {
		t.Fatalf("quiet_bd_calls missing or not int64: %#v", parts)
	}

	got := phase.withBDCost(parts)
	for _, key := range []string{"quiet_ms", "busy_ms", "busy_bd_calls", "bd_calls", "bd_ms"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("record lost %s: %#v", key, got)
		}
	}
}
