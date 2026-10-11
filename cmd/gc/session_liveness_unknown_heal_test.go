package main

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// staleTmuxCacheProvider answers the way the tmux provider does once
// list-panes has failed for longer than the state cache's staleTTL (30s): the
// bool read (IsRunning) has fallen off the cliff and says every session is
// gone, while the error-bearing read says it cannot tell. The session is
// never started in the embedded Fake, so the bool read is "absent" for real.
type staleTmuxCacheProvider struct {
	*runtime.Fake
}

func (p *staleTmuxCacheProvider) ObserveLivenessWithError(name string, _ []string) (runtime.Liveness, error) {
	return runtime.Liveness{}, fmt.Errorf("observing tmux session %q: state cache cannot vouch for it: %w", name, runtime.ErrRuntimeUnavailable)
}

// A failed list-panes read is not proof that a session is gone (vn-fk6253e).
// Past staleTTL the tmux bool read reports every session absent; if the
// reconciler heal read that, it would put every live agent to sleep with
// sleep_reason=runtime-missing and reset its resume key. The heal must read
// the error-bearing observation and leave the session alone. The control
// tick proves the same session IS healed when absence is confirmed, so the
// first assertion is not passing for some unrelated reason.
func TestReconcileSessionBeadsUnknownLivenessNeverWritesRuntimeMissing(t *testing.T) {
	setup := func(t *testing.T) (*reconcilerTestEnv, beads.Bead) {
		t.Helper()
		env := newReconcilerTestEnv()
		env.cfg = &config.City{
			Workspace: config.Workspace{Name: "test-city"},
			Agents:    []config.Agent{{Name: "worker", StartCommand: "test-cmd"}},
		}
		env.addDesired("worker", "worker", false)
		session := env.createSessionBead("worker", "worker")
		env.setSessionMetadata(&session, map[string]string{
			"state":               "active",
			"last_woke_at":        env.clk.Now().Add(-wakeUndesiredGrace - time.Minute).UTC().Format(time.RFC3339),
			"session_key":         "resume-worker",
			"started_config_hash": "config-worker",
		})
		before, err := env.store.Get(session.ID)
		if err != nil {
			t.Fatalf("Get before reconcile: %v", err)
		}
		return env, before
	}
	tick := func(env *reconcilerTestEnv, session beads.Bead, sp runtime.Provider) beads.Bead {
		t.Helper()
		reconcileSessionBeads(
			context.Background(), []beads.Bead{session}, env.desiredState,
			configuredSessionNames(env.cfg, "", env.store), env.cfg, sp, env.store,
			nil, nil, nil, env.dt, map[string]int{"worker": 1}, false, nil, "", nil,
			env.clk, env.rec, 0, 0, &env.stdout, &env.stderr, env.startOptions...,
		)
		after, err := env.store.Get(session.ID)
		if err != nil {
			t.Fatalf("Get after reconcile: %v", err)
		}
		return after
	}

	t.Run("unknown liveness leaves the session alone", func(t *testing.T) {
		env, before := setup(t)
		sp := &staleTmuxCacheProvider{Fake: env.sp}
		if sp.IsRunning("worker") {
			t.Fatal("fixture: the bool read must already say absent, as tmux does past staleTTL")
		}
		after := tick(env, before, sp)
		if got := after.Metadata["sleep_reason"]; got == string(sessionpkg.SleepReasonRuntimeMissing) {
			t.Fatalf("unknown liveness wrote sleep_reason=%q", got)
		}
		if after.Status != before.Status || !maps.Equal(after.Metadata, before.Metadata) {
			t.Fatalf("unknown liveness mutated the session: before=%#v after=%#v", before.Metadata, after.Metadata)
		}
		if n := sp.CountCalls("Start", "worker"); n != 0 {
			t.Fatalf("unknown liveness started a second copy of a session that may be live: Start calls = %d", n)
		}
		if ds := env.dt.get(before.ID); ds != nil {
			t.Fatalf("unknown liveness started a drain: reason=%q", ds.reason)
		}
	})

	t.Run("control: confirmed absence is healed", func(t *testing.T) {
		env, before := setup(t)
		after := tick(env, before, env.sp)
		if maps.Equal(after.Metadata, before.Metadata) && env.sp.CountCalls("Start", "worker") == 0 {
			t.Fatal("confirmed absence changed nothing, so the unknown case above proves nothing")
		}
	})
}
