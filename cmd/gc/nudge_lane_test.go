package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// newNudgeLaneRuntime builds a directly-constructed runtime with one running
// "worker" session and one due nudge queued for it. No run loop and no tick
// run: whatever delivers the nudge, the lane does it alone.
func newNudgeLaneRuntime(t *testing.T, cfg *config.City) (*CityRuntime, *runtime.Fake, *synchronizedBuffer) {
	t.Helper()
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()

	store := openNudgeBeadStore(dir)
	fake := runtime.NewFake()
	mgr := newSessionManagerWithConfig(dir, store.Store, fake, nil)
	info, err := mgr.CreateSession(context.Background(), session.CreateOptions{Template: "worker", Title: "Worker", Command: "codex", WorkDir: dir, Provider: "codex", Env: nil, Resume: session.ProviderResume{}, Hints: runtime.Config{WorkDir: dir}, ExtraMeta: map[string]string{"session_origin": "manual"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.Start(context.Background(), info.ID, "", runtime.Config{WorkDir: dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fake.Activity = map[string]time.Time{info.SessionName: time.Now().Add(-10 * time.Second)}

	if err := enqueueQueuedNudge(dir, newQueuedNudge("worker", "send the REVIEW line", time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}

	stderr := &synchronizedBuffer{}
	cr := &CityRuntime{
		cityName:    "test-city",
		cityPath:    dir,
		cfg:         cfg,
		sp:          fake,
		logPrefix:   "gc",
		stderr:      stderr,
		nudgeWakeCh: make(chan struct{}, 1),
	}
	cr.setStandaloneStores(store.Store, nil)
	return cr, fake, stderr
}

// supervisorCfgWithPatrol is supervisorCfg with the lane's backstop interval.
func supervisorCfgWithPatrol(interval string) *config.City {
	cfg := supervisorCfg()
	cfg.Daemon.PatrolInterval = interval
	return cfg
}

func nudgeCallMessages(fake *runtime.Fake) []string {
	var out []string
	for _, call := range fake.SnapshotCalls() {
		if call.Method == "Nudge" {
			out = append(out, call.Message)
		}
	}
	return out
}

func waitForNudgeCalls(t *testing.T, fake *runtime.Fake, within time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if msgs := nudgeCallMessages(fake); len(msgs) > 0 {
			return msgs
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// Kills: queued-nudge delivery depending on the controller goroutine
// (vn-3dy94f5). One config reload held a patrol tick for 2h28m and no queued
// nudge was tried, because the dispatcher ran only from the run loop's wake
// arm and the end of the tick. Here no run loop and no tick exist at all: a
// wake must reach the lane goroutine and the lane must type the nudge and ack
// it by itself. The patrol interval is an hour so only the wake can explain a
// delivery.
func TestNudgeLaneDeliversWithNoTickRunning(t *testing.T) {
	cr, fake, stderr := newNudgeLaneRuntime(t, supervisorCfgWithPatrol("1h"))
	ctx, cancel := context.WithCancel(context.Background())
	done := cr.startNudgeLane(ctx)
	defer func() {
		cancel()
		<-done
	}()

	cr.wakeNudgeLane()

	msgs := waitForNudgeCalls(t, fake, 10*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("nudge calls = %d, want 1 (stderr: %s)", len(msgs), stderr.String())
	}
	if !strings.Contains(msgs[0], "send the REVIEW line") {
		t.Fatalf("nudge message = %q, want the queued text", msgs[0])
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		pending, inFlight, dead, err := listQueuedNudges(cr.cityPath, "worker", time.Now())
		if err != nil {
			t.Fatalf("listQueuedNudges: %v", err)
		}
		if len(pending) == 0 && len(inFlight) == 0 && len(dead) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue not drained: pending=%d inFlight=%d dead=%d", len(pending), len(inFlight), len(dead))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, reason, ran := cr.nudgeLaneOf().lastPass(); !ran || reason != nudgeLaneReasonWake {
		t.Fatalf("lastPass = (ran=%v, reason=%q), want a wake pass", ran, reason)
	}
}

// Kills: the tick's nudge step dispatching on the controller goroutine again.
// It may only wake the lane: with no lane running, the nudge stays queued,
// nothing is typed, and the wake is waiting on the lane's channel.
func TestRunWakeNudgeLaneOnlyWakes(t *testing.T) {
	cr, fake, _ := newNudgeLaneRuntime(t, supervisorCfgWithPatrol("1h"))

	cr.runWakeNudgeLane(nil)

	if msgs := nudgeCallMessages(fake); len(msgs) != 0 {
		t.Fatalf("tick step typed %d nudge(s) on the controller goroutine, want 0", len(msgs))
	}
	pending, _, _, err := listQueuedNudges(cr.cityPath, "worker", time.Now())
	if err != nil {
		t.Fatalf("listQueuedNudges: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want the nudge left for the lane", len(pending))
	}
	select {
	case <-cr.nudgeWakeCh:
	default:
		t.Fatal("tick step did not wake the nudge lane")
	}
}

// Kills: a lane that only runs when woken. A missed wake (socket race,
// listener restart) must still be delivered by the backstop timer one patrol
// interval later, with no wake and no tick.
func TestNudgeLaneBackstopDeliversWithoutWake(t *testing.T) {
	cr, fake, stderr := newNudgeLaneRuntime(t, supervisorCfgWithPatrol("50ms"))
	ctx, cancel := context.WithCancel(context.Background())
	done := cr.startNudgeLane(ctx)
	defer func() {
		cancel()
		<-done
	}()

	if msgs := waitForNudgeCalls(t, fake, 10*time.Second); len(msgs) != 1 {
		t.Fatalf("nudge calls = %d, want 1 from the backstop (stderr: %s)", len(msgs), stderr.String())
	}
	if _, reason, ran := cr.nudgeLaneOf().lastPass(); !ran || reason != nudgeLaneReasonCadence {
		t.Fatalf("lastPass = (ran=%v, reason=%q), want a cadence pass", ran, reason)
	}
}

// Kills: the lane delivering in legacy mode, where the per-session
// `gc nudge poll` processes own delivery and two drains would race.
func TestNudgeLanePassIsNoOpInLegacyMode(t *testing.T) {
	cr, fake, _ := newNudgeLaneRuntime(t, &config.City{})

	cr.runNudgeLanePass(context.Background(), nudgeLaneReasonWake)

	if msgs := nudgeCallMessages(fake); len(msgs) != 0 {
		t.Fatalf("legacy-mode lane typed %d nudge(s), want 0", len(msgs))
	}
	if _, _, ran := cr.nudgeLaneOf().lastPass(); ran {
		t.Fatal("legacy-mode lane recorded a dispatcher pass")
	}
}
