package beads

import (
	"testing"
	"time"
)

// TestBDExecMeterCountsEveryTraceBDCall proves the meter counts a finished bd
// call even when the JSONL trace is off, since the tick's bd_calls and bd_ms
// fields depend on it.
func TestBDExecMeterCountsEveryTraceBDCall(t *testing.T) {
	t.Setenv("GC_BD_TRACE_JSON", "")
	before := ReadBDExecTotals()
	TraceBDCall("go:test", t.TempDir(), []string{"list"}, time.Now().Add(-20*time.Millisecond), 0, nil)
	got := ReadBDExecTotals().Since(before)
	if got.Calls < 1 {
		t.Fatalf("meter calls = %d, want at least 1", got.Calls)
	}
	if got.Elapsed < 20*time.Millisecond {
		t.Fatalf("meter elapsed = %v, want at least 20ms", got.Elapsed)
	}
}
