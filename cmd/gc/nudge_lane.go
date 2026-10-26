package main

// The nudge lane: the supervisor nudge dispatcher on its own goroutine.
//
// # What was wrong (vn-3dy94f5)
//
// The dispatcher used to run only on the controller goroutine: from the run
// loop's select arm for the wake socket, and at the end of every reconcile
// tick as a fallback. Both waited for the tick in flight. On 2026-10-26 one
// patrol tick spent 8,903s (2h28m) in its config reload, before its first
// session phase. For that whole time no queued nudge was tried: they sat in
// Pending with attempts=0 and a zero last_attempt_at, squad reviewers never
// got their REVIEW lines, and `gc session nudge` had told every sender "the
// queued dispatcher delivers it". The orders and inventory lanes, already on
// their own goroutines, ran the whole time. hq-02cr3 is the same symptom with
// earlier causes, so a stalled queue is the first thing to suspect when a
// tick is slow.
//
// # The lane
//
// The lane owns delivery. The wake socket feeds it directly (cr.nudgeWakeCh),
// a backstop timer runs a pass one patrol interval after the last one ended,
// and the tick only wakes it (wakeNudgeLanePhase), as it wakes the orders
// lane. A slow or wedged tick now delays a queued nudge by at most one patrol
// interval. Do not add a dispatch call back onto the controller goroutine:
// that is the coupling this lane removes.
//
// A pass reads the config and provider a reload last published, as one pair
// (serviceProviderSnapshot), and resolves its stores from that config. It
// never reads cr.cfg or cr.sp, which a reload writes on the controller
// goroutine. Delivering beside the tick, the API and the CLI is safe: the
// queue claim is atomic across every drain path
// (claimDueQueuedNudgesForTarget), and the provider holds a per-session nudge
// lock while it types.

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// nudgeLaneSafeTickTrigger names the lane in safeTick panic lines.
	nudgeLaneSafeTickTrigger = "nudge-lane"

	nudgeLaneReasonCadence = "cadence"
	nudgeLaneReasonWake    = "wake"
)

// nudgeLane records the lane's last pass, for the tick trace. The wake
// channel is cr.nudgeWakeCh, which the wake socket listener already feeds.
type nudgeLane struct {
	statusMu       sync.Mutex
	lastPassAt     time.Time
	lastPassReason string
	passRan        bool
}

// nudgeLaneOf returns this runtime's lane, creating it on first use so a
// directly-constructed CityRuntime needs no wiring.
func (cr *CityRuntime) nudgeLaneOf() *nudgeLane {
	cr.nudgeLaneOnce.Do(func() { cr.nudgeLane = &nudgeLane{} })
	return cr.nudgeLane
}

// notePass records a pass that reached the dispatcher.
func (l *nudgeLane) notePass(at time.Time, reason string) {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	l.lastPassAt, l.lastPassReason, l.passRan = at, reason, true
}

// lastPass reports when the last pass that reached the dispatcher finished,
// what triggered it, and whether one has at all. A growing age in the tick
// trace is a stalled lane.
func (l *nudgeLane) lastPass() (at time.Time, reason string, ran bool) {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	return l.lastPassAt, l.lastPassReason, l.passRan
}

// wakeNudgeLane asks the lane for a pass. Non-blocking; harmless when no lane
// goroutine is running.
func (cr *CityRuntime) wakeNudgeLane() {
	select {
	case cr.nudgeWakeCh <- struct{}{}:
	default:
	}
}

// startNudgeLane starts the lane goroutine and returns a channel closed when
// it exits (ctx done). It is paced by startPacedLane on the patrol interval,
// read once, as the tick's ticker reads it.
func (cr *CityRuntime) startNudgeLane(ctx context.Context) <-chan struct{} {
	interval := cr.serviceConfigSnapshot().Daemon.PatrolIntervalDuration()
	return startPacedLane(ctx, interval, 0, cr.nudgeWakeCh, func(wake bool) {
		reason := nudgeLaneReasonCadence
		if wake {
			reason = nudgeLaneReasonWake
		}
		cr.safeTick(func() {
			cr.runNudgeLanePass(ctx, reason)
		}, nudgeLaneSafeTickTrigger)
	})
}

// runNudgeLanePass is one dispatcher pass. A city in legacy mode, where the
// per-session `gc nudge poll` processes own delivery, makes it a no-op.
//
// The session snapshot is a plain store read. Seeding the provider's ACP
// routes from it stays the tick's job: every tick's snapshot load seeds them
// (seedACPRoutes), so the lane does not pay that config walk a second time.
func (cr *CityRuntime) runNudgeLanePass(ctx context.Context, reason string) {
	if ctx.Err() != nil || cr.cityPath == "" {
		return
	}
	cfg, sp := cr.serviceProviderSnapshot()
	if !nudgeDispatcherIsSupervisor(cfg) {
		return
	}
	city := cr.cityBeadStore()
	if city == nil {
		return
	}
	nudges := resolveNudgesStore(cr.storageRoutes, city, cfg, cr.cityPath, cr.rec)
	sessStore := resolveSessionStore(cr.storageRoutes, city, cfg, cr.cityPath, cr.rec)
	if nudges == nil || sessStore == nil {
		return
	}
	sessionBeads, err := loadSessionBeadSnapshot(sessStore)
	if err != nil {
		fmt.Fprintf(cr.stderr, "%s: nudge dispatcher: loading session beads: %v\n", cr.logPrefix, err) //nolint:errcheck // best-effort stderr
		return
	}
	if _, err := dispatchAllQueuedNudges(cr.cityPath, cfg, nudges, sessStore, sp, sessionBeads, cr.stderr); err != nil {
		fmt.Fprintf(cr.stderr, "%s: nudge dispatcher: %v\n", cr.logPrefix, err) //nolint:errcheck // best-effort stderr
	}
	cr.nudgeLaneOf().notePass(time.Now(), reason)
}
