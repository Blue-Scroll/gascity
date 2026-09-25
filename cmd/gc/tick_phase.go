package main

import (
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// tickPhaseStart marks where one controller-tick phase began: the wall clock,
// and the bd exec meter at that moment. Start a phase with startTickPhase and
// record it with RecordTickPhase; the record then carries bd_calls and bd_ms
// beside duration_ms.
//
// Those two fields answer the first question about a slow phase: was it slow
// because it ran many bd calls (each a new process and a Dolt round trip), or
// for some other reason (a lock, a tmux or ps call, plain compute)? A phase
// whose bd_ms is close to its duration_ms is paying for store reads; one whose
// bd_ms is small is not.
type tickPhaseStart struct {
	at time.Time
	bd beads.BDExecTotals
}

func startTickPhase() tickPhaseStart {
	return tickPhaseStart{at: time.Now(), bd: beads.ReadBDExecTotals()}
}

func (s tickPhaseStart) elapsed() time.Duration {
	return time.Since(s.at)
}

// withBDCost returns a copy of fields plus bd_calls and bd_ms for the phase
// that began at s. The meter is process-wide, so the counts can include a few
// calls from background lanes that ran at the same time (see bd_exec_meter.go).
func (s tickPhaseStart) withBDCost(fields map[string]any) map[string]any {
	cost := beads.ReadBDExecTotals().Since(s.bd)
	out := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		out[k] = v
	}
	out["bd_calls"] = cost.Calls
	out["bd_ms"] = cost.Elapsed.Milliseconds()
	return out
}

// RecordTickPhase records one completed controller-tick phase that began at
// start, with its bd cost. It is safe to call on a nil cycle.
func (c *SessionReconcilerTraceCycle) RecordTickPhase(site TraceSiteCode, name string, start tickPhaseStart, fields map[string]any) {
	if c == nil {
		return
	}
	c.RecordControllerOperation(site, TraceReasonRetained, TraceOutcomeComplete, name, start.elapsed(), start.withBDCost(fields))
}
