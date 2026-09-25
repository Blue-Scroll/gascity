package beads

import (
	"sync/atomic"
	"time"
)

// The bd exec meter counts every bd subprocess this process has finished, and
// the wall time they took. A bd call is the unit of cost the controller tick
// pays for store reads: each one starts a new process and makes a Dolt round
// trip, and on a busy box that is seconds, not milliseconds. The tick reads the
// meter at the start and end of each phase, so a phase record can say "this
// phase ran 36 bd calls that took 41s" instead of only "this phase took 41s".
//
// TraceBDCall is the one call every bd subprocess path makes when it finishes,
// so it is where the meter counts. A new path that runs bd must call
// TraceBDCall too, or this meter (and the JSONL trace) will not see it.
//
// The counters are process-wide. Anything else running bd at the same moment
// (the cache reconcile loop, the bead event watcher) lands in the same totals,
// so a phase's count can be a little high. It is never low.
var (
	bdExecCalls atomic.Int64
	bdExecNanos atomic.Int64
)

// BDExecTotals is one reading of the process-wide bd exec meter.
type BDExecTotals struct {
	// Calls is how many bd subprocesses have finished.
	Calls int64
	// Elapsed is their summed wall time. Calls that ran at the same time each
	// add their full time, so this can exceed the wall time between readings.
	Elapsed time.Duration
}

// ReadBDExecTotals returns the meter's current totals.
func ReadBDExecTotals() BDExecTotals {
	return BDExecTotals{
		Calls:   bdExecCalls.Load(),
		Elapsed: time.Duration(bdExecNanos.Load()),
	}
}

// Since returns what the meter recorded between an earlier reading and t.
func (t BDExecTotals) Since(earlier BDExecTotals) BDExecTotals {
	return BDExecTotals{
		Calls:   t.Calls - earlier.Calls,
		Elapsed: t.Elapsed - earlier.Elapsed,
	}
}

// noteBDExec adds one finished bd subprocess that ran for elapsed.
func noteBDExec(elapsed time.Duration) {
	bdExecCalls.Add(1)
	bdExecNanos.Add(int64(elapsed))
}
