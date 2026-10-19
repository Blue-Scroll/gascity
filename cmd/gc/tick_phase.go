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

// tickPhaseParts holds the fields of one tick phase record that break the
// phase into named parts. Each part adds <name>_ms and <name>_bd_calls, so a
// slow phase's record says WHICH part was slow and whether that part was
// paying for bd calls. Pass it as the fields of RecordTickPhase.
//
// Use it on a phase that does several unrelated things in a row. The
// dispatch_orders phase is the first: it ran three sweeps, a rescan and the
// order gates under one 20s to 221s number, and the trace could not say which
// (hq-mb1gvr).
type tickPhaseParts map[string]any

// time runs fn and records it as the part called name.
func (p tickPhaseParts) time(name string, fn func()) {
	start := startTickPhase()
	fn()
	p.record(name, start)
}

// record records the part called name as running from start until now.
func (p tickPhaseParts) record(name string, start tickPhaseStart) {
	p[name+"_ms"] = start.elapsed().Milliseconds()
	p[name+"_bd_calls"] = beads.ReadBDExecTotals().Since(start.bd).Calls
}

// tickPhaseLaps splits a tick phase into named regions when the phase is a
// loop that leaves its turns early. Call enter(name) where each region
// begins: it closes the region that was open and opens the new one. Call
// addTo once after the loop to close the last region and write the totals.
//
// Each region adds three fields, summed over every turn of the loop:
// <name>_ms, <name>_bd_calls, and <name>_max_ms, the slowest single turn
// spent in it. So a slow phase says which region was slow, whether it was
// paying for bd calls, and whether one session or all of them made it slow.
//
// Use it instead of tickPhaseParts when a turn can `continue` out of the
// middle. A `continue` needs no extra call: the time stays charged to the
// region the turn was in, and the next turn's first enter closes it. No time
// in the phase goes uncounted. The forward pass is the first user: one loop
// of about 2,000 lines, with a `continue` in most of its blocks, timed as one
// 8 s median number with bd_ms near 0 (vn-m4h54cp).
//
// Durations add up in nanoseconds and become milliseconds only in addTo, so
// a region run 100 times at 0.4 ms each reads 40, not 0.
type tickPhaseLaps struct {
	open    string
	start   tickPhaseStart
	regions map[string]*tickPhaseLap
}

type tickPhaseLap struct {
	elapsed time.Duration
	slowest time.Duration
	bdCalls int64
}

// enter closes the open region, if any, and opens the region called name.
func (l *tickPhaseLaps) enter(name string) {
	l.closeOpen()
	l.open = name
	l.start = startTickPhase()
}

func (l *tickPhaseLaps) closeOpen() {
	if l.open == "" {
		return
	}
	if l.regions == nil {
		l.regions = map[string]*tickPhaseLap{}
	}
	lap := l.regions[l.open]
	if lap == nil {
		lap = &tickPhaseLap{}
		l.regions[l.open] = lap
	}
	elapsed := l.start.elapsed()
	lap.elapsed += elapsed
	lap.slowest = max(lap.slowest, elapsed)
	lap.bdCalls += beads.ReadBDExecTotals().Since(l.start.bd).Calls
	l.open = ""
}

// addTo closes the open region and writes every region's totals into fields.
func (l *tickPhaseLaps) addTo(fields tickPhaseParts) {
	l.closeOpen()
	for name, lap := range l.regions {
		fields[name+"_ms"] = lap.elapsed.Milliseconds()
		fields[name+"_max_ms"] = lap.slowest.Milliseconds()
		fields[name+"_bd_calls"] = lap.bdCalls
	}
}
