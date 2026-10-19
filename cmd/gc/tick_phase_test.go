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

// TestTickPhaseLapsChargesAContinueToItsRegion proves a turn that leaves the
// loop early keeps its time and its bd calls in the region it was in, that
// each region sums over every turn, and that the slowest turn is kept apart.
func TestTickPhaseLapsChargesAContinueToItsRegion(t *testing.T) {
	t.Setenv("GC_BD_TRACE_JSON", "")
	var laps tickPhaseLaps
	for i := range 3 {
		laps.enter("prelude")
		laps.enter("slow")
		beads.TraceBDCall("go:test", t.TempDir(), []string{"list"}, time.Now(), 0, nil)
		time.Sleep(2 * time.Millisecond)
		if i < 2 {
			continue
		}
		laps.enter("tail")
	}
	fields := tickPhaseParts{"ordered_session_count": 3}
	laps.addTo(fields)

	if ms, _ := fields["slow_ms"].(int64); ms < 6 {
		t.Fatalf("slow_ms = %v, want at least 6 (3 turns of 2ms, 2 of them via continue)", fields["slow_ms"])
	}
	if ms, _ := fields["slow_max_ms"].(int64); ms < 2 {
		t.Fatalf("slow_max_ms = %v, want at least 2", fields["slow_max_ms"])
	}
	if calls, _ := fields["slow_bd_calls"].(int64); calls < 3 {
		t.Fatalf("slow_bd_calls = %v, want at least 3", fields["slow_bd_calls"])
	}
	for _, key := range []string{"prelude_ms", "prelude_max_ms", "prelude_bd_calls", "tail_ms", "tail_max_ms", "tail_bd_calls"} {
		if _, ok := fields[key].(int64); !ok {
			t.Fatalf("%s missing or not int64: %#v", key, fields)
		}
	}
	if fields["ordered_session_count"] != 3 {
		t.Fatalf("caller field lost: %#v", fields)
	}
}

// TestTickPhaseLapsSumsBeforeRounding proves many sub-millisecond laps add up
// instead of each rounding down to 0 ms.
func TestTickPhaseLapsSumsBeforeRounding(t *testing.T) {
	var laps tickPhaseLaps
	for range 10 {
		laps.enter("short")
		time.Sleep(300 * time.Microsecond)
	}
	fields := tickPhaseParts{}
	laps.addTo(fields)
	if ms, _ := fields["short_ms"].(int64); ms < 3 {
		t.Fatalf("short_ms = %v, want at least 3 (10 laps of 0.3ms)", fields["short_ms"])
	}
}

// TestTickPhaseLapsWithNoRegions proves a loop that never ran writes nothing.
func TestTickPhaseLapsWithNoRegions(t *testing.T) {
	var laps tickPhaseLaps
	fields := tickPhaseParts{"wake_target_count": 0}
	laps.addTo(fields)
	if len(fields) != 1 {
		t.Fatalf("empty laps wrote fields: %#v", fields)
	}
}
