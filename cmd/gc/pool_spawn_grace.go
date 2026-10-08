package main

import (
	"fmt"
	"io"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// poolSpawnGrace is how long a freshly started pool seat is safe from being
// retired for lack of demand.
//
// Why it exists (hq-qufuy, vn-n5abuk0). Pool demand counts beads that are
// routed to the pool AND unassigned. So the seat's own claim is what zeroes
// the demand that spawned it:
//
//  1. a bead is routed and unassigned, so demand reads 1
//  2. the reconciler spawns a seat
//  3. the seat runs `gc hook --claim` and claims the bead
//  4. the bead is assigned now, so demand reads 0
//  5. the reconciler sees a live seat with no demand and retires it
//  6. the drain releases the claim, demand reads 1 again, back to step 2
//
// Measured on 2026-08-23: about 7 spawns in 15 minutes, zero work done. The
// seats died about 83 seconds old, still inside their first `gc hook`. The
// claim does count as the seat's own work once the reconciler can see it, but
// on 2026-09-22 that took about 2 minutes (a no-wake-reason drain at 17:50:36Z,
// cancelled for assigned work at 17:52:27Z). Any retirement inside that window
// kills a seat that is doing exactly what it was spawned to do.
//
// So a seat younger than this is never retired on a demand reading. 83 seconds
// is the floor, not the target: 5 minutes covers the measured 2-minute window
// more than twice over. A seat that finds no work still stops itself
// (drain-ack), which this does not touch, so the cost of the grace is at most
// 5 idle minutes on a seat that was never needed.
//
// Casey ruled this on 2026-09-22 (option A of three). It deliberately does NOT
// change how demand is counted (option B), and does NOT retire on idleness seen
// over a window (option C).
const poolSpawnGrace = 5 * time.Minute

// poolSeatInSpawnGrace reports whether info is a pool seat whose current awake
// interval started less than poolSpawnGrace ago, and how old it is.
//
// Only a demand-driven retirement may consult it: the no-wake-reason drain, and
// the orphan drain of a seat whose pool still exists. A suspend, a config
// drift, a stalled-execution drain or the agent's own drain-ack must never
// wait on it.
//
// It fails OPEN to the old behavior (retire allowed) when it cannot prove the
// seat is young: not pool-managed, no pool agent in config, a suspended agent,
// no parseable start time, or a start time in the future. A guard that could
// hold a seat forever on a garbled timestamp would trade a churn bug for a
// stuck-slot bug.
func poolSeatInSpawnGrace(info sessionpkg.Info, cfg *config.City, now time.Time) (time.Duration, bool) {
	if !isPoolManagedSessionInfo(info) {
		return 0, false
	}
	template := normalizedSessionTemplateInfo(info, cfg)
	if template == "" {
		template = info.Template
	}
	agent := findAgentByTemplate(cfg, template)
	if agent == nil || agent.Suspended {
		return 0, false
	}
	// awake_started_at is stamped on every confirmed start and never cleared,
	// so it is the start of THIS awake interval, even on a reused pool bead.
	// creation_complete_at is written by the same start and is the fallback for
	// a bead from an older binary that never stamped awake_started_at.
	started, ok := parseRFC3339Metadata(info.AwakeStartedAt)
	if !ok {
		started, ok = parseRFC3339Metadata(info.CreationCompleteAt)
	}
	if !ok || started.After(now) {
		return 0, false
	}
	age := now.Sub(started)
	return age, age < poolSpawnGrace
}

// deferPoolSeatRetireForSpawnGrace is the one place a demand-driven retire site
// asks the grace question. It returns true when the seat must be left alone
// this tick, and says so in the trace and on stdout, so a held seat is never a
// silent one. Both drain sites call this rather than poolSeatInSpawnGrace, so
// the record they write cannot drift apart.
func deferPoolSeatRetireForSpawnGrace(
	info sessionpkg.Info,
	cfg *config.City,
	clk clock.Clock,
	trace *sessionReconcilerTraceCycle,
	site TraceSiteCode,
	reason, name string,
	stdout io.Writer,
) bool {
	age, inGrace := poolSeatInSpawnGrace(info, cfg, clk.Now())
	if !inGrace {
		return false
	}
	if trace != nil {
		template := normalizedSessionTemplateInfo(info, cfg)
		if template == "" {
			template = info.Template
		}
		trace.RecordDecision(site, TraceReasonCode(reason), TraceOutcomeDeferredSpawnGrace, template, name, traceRecordPayload{
			"age_seconds":   int(age.Seconds()),
			"grace_seconds": int(poolSpawnGrace.Seconds()),
		})
	}
	fmt.Fprintf(stdout, "Deferring %s drain for '%s': pool seat started %s ago, inside its %s spawn grace\n", //nolint:errcheck
		reason, name, age.Round(time.Second), poolSpawnGrace)
	return true
}
