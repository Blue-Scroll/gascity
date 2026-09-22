package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// spawnGraceSeat is one live pool seat of template "worker", started at
// env.clk.Now(), sitting in the desired set the way a spawned seat does.
type spawnGraceSeat struct {
	env     *reconcilerTestEnv
	session beads.Bead
	name    string
	started time.Time
}

func newSpawnGraceSeat(t *testing.T, suspended bool) *spawnGraceSeat {
	t.Helper()
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              "worker",
			StartCommand:      "true",
			MinActiveSessions: intPtr(0),
			MaxActiveSessions: intPtr(2),
			Suspended:         suspended,
		}},
	}
	name := "worker-1"
	env.addDesired(name, "worker", true)
	session := env.createSessionBead(name, "worker")
	started := env.clk.Now().UTC()
	env.setSessionMetadata(&session, map[string]string{
		"pool_managed":         "true",
		"state":                string(sessionpkg.StateActive),
		"state_reason":         "creation_complete",
		"creation_complete_at": started.Format(time.RFC3339),
		"awake_started_at":     started.Format(time.RFC3339),
		"last_woke_at":         started.Format(time.RFC3339),
	})
	return &spawnGraceSeat{env: env, session: session, name: name, started: started}
}

// tick runs one reconciler pass with the given pool demand for "worker".
func (s *spawnGraceSeat) tick(t *testing.T, demand int) {
	t.Helper()
	sessions, err := loadSessionBeads(s.env.store)
	if err != nil {
		t.Fatalf("loading session beads: %v", err)
	}
	s.env.reconcileWithPoolDesired(sessions, map[string]int{"worker": demand})
}

func (s *spawnGraceSeat) drain() *drainState { return s.env.dt.get(s.session.ID) }

// TestPoolSpawnGrace_SeatSurvivesItsOwnClaim reproduces the hq-qufuy loop. The
// seat's claim takes its bead out of the "routed and unassigned" set, so demand
// reads 0 while the claim is still on its way to being visible as the seat's
// own work. Before the grace, the first such tick began a no-wake-reason drain
// (measured deaths at ~83s), the drain released the claim, and the pool spawned
// again. The seat must come through that whole window with no drain at all.
func TestPoolSpawnGrace_SeatSurvivesItsOwnClaim(t *testing.T) {
	seat := newSpawnGraceSeat(t, false)

	// Step 1-2: a routed, unassigned bead is demand 1 and the seat is awake.
	seat.tick(t, 1)
	if ds := seat.drain(); ds != nil {
		t.Fatalf("a seat with demand was drained: %+v", ds)
	}

	// Steps 3-5: the seat claims the bead, so demand reads 0, and the
	// reconciler cannot see the claim as the seat's work yet. Walk the whole
	// measured window: the 83s deaths and the ~2 minute claim lag.
	for _, at := range []time.Duration{30 * time.Second, 83 * time.Second, 2 * time.Minute, poolSpawnGrace - time.Second} {
		seat.env.clk.Time = seat.started.Add(at)
		seat.tick(t, 0)
		if ds := seat.drain(); ds != nil {
			t.Fatalf("seat drained at age %s with reason %q; its own claim zeroed the demand (hq-qufuy)\nstdout:\n%s",
				at, ds.reason, seat.env.stdout.String())
		}
		if !seat.env.sp.IsRunning(seat.name) {
			t.Fatalf("seat stopped at age %s", at)
		}
	}
	if !strings.Contains(seat.env.stdout.String(), "Deferring no-wake-reason drain for 'worker-1'") {
		t.Fatalf("a held seat must say why it was held; stdout:\n%s", seat.env.stdout.String())
	}
}

// TestPoolSpawnGrace_IdleSeatRetiresAfterGrace proves the grace is a window,
// not a mute switch: a seat that is still unneeded once the grace has passed is
// retired exactly as before.
func TestPoolSpawnGrace_IdleSeatRetiresAfterGrace(t *testing.T) {
	seat := newSpawnGraceSeat(t, false)

	seat.env.clk.Time = seat.started.Add(poolSpawnGrace)
	seat.tick(t, 0)

	ds := seat.drain()
	if ds == nil {
		t.Fatalf("a seat past its grace with no demand was not retired\nstdout:\n%s", seat.env.stdout.String())
	}
	if ds.reason != "no-wake-reason" {
		t.Fatalf("drain reason = %q, want no-wake-reason", ds.reason)
	}
}

// TestPoolSpawnGrace_SleepIntentIsNotHeld: a young seat that asked to sleep is
// telling us about itself, not reading demand. The grace must not hold it.
func TestPoolSpawnGrace_SleepIntentIsNotHeld(t *testing.T) {
	seat := newSpawnGraceSeat(t, false)
	seat.env.setSessionMetadata(&seat.session, map[string]string{"sleep_intent": "user-hold"})

	seat.env.clk.Time = seat.started.Add(30 * time.Second)
	seat.tick(t, 0)

	ds := seat.drain()
	if ds == nil {
		t.Fatalf("a young seat with a sleep intent was held by the spawn grace\nstdout:\n%s", seat.env.stdout.String())
	}
	if ds.reason != "user-hold" {
		t.Fatalf("drain reason = %q, want user-hold", ds.reason)
	}
}

// TestPoolSpawnGrace_OrphanScaleDownIsHeld covers the other demand-driven
// retire site: a young seat that left the desired set while its pool still
// exists. A suspended pool is not a demand reading and must still drain now.
func TestPoolSpawnGrace_OrphanScaleDownIsHeld(t *testing.T) {
	for _, tc := range []struct {
		name      string
		suspended bool
		wantDrain bool
	}{
		{name: "pool still configured", suspended: false, wantDrain: false},
		{name: "pool suspended", suspended: true, wantDrain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seat := newSpawnGraceSeat(t, tc.suspended)
			delete(seat.env.desiredState, seat.name)

			seat.env.clk.Time = seat.started.Add(83 * time.Second)
			seat.tick(t, 0)

			ds := seat.drain()
			if tc.wantDrain && ds == nil {
				t.Fatalf("seat was not drained\nstdout:\n%s", seat.env.stdout.String())
			}
			if !tc.wantDrain && ds != nil {
				t.Fatalf("young seat drained with reason %q\nstdout:\n%s", ds.reason, seat.env.stdout.String())
			}
		})
	}
}

func TestPoolSeatInSpawnGrace(t *testing.T) {
	now := time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC)
	cfg := &config.City{Agents: []config.Agent{
		{Name: "worker"},
		{Name: "paused", Suspended: true},
	}}
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	seat := func(template string, meta func(*sessionpkg.Info)) sessionpkg.Info {
		info := sessionpkg.Info{Template: template, PoolManaged: true, AwakeStartedAt: at(-time.Minute)}
		if meta != nil {
			meta(&info)
		}
		return info
	}

	for _, tc := range []struct {
		name string
		info sessionpkg.Info
		want bool
	}{
		{"young seat", seat("worker", nil), true},
		{"one second before the edge", seat("worker", func(i *sessionpkg.Info) { i.AwakeStartedAt = at(-poolSpawnGrace + time.Second) }), true},
		{"exactly at the edge", seat("worker", func(i *sessionpkg.Info) { i.AwakeStartedAt = at(-poolSpawnGrace) }), false},
		{"old seat", seat("worker", func(i *sessionpkg.Info) { i.AwakeStartedAt = at(-time.Hour) }), false},
		{"falls back to creation_complete_at", seat("worker", func(i *sessionpkg.Info) {
			i.AwakeStartedAt = ""
			i.CreationCompleteAt = at(-time.Minute)
		}), true},
		{"no start time fails open", seat("worker", func(i *sessionpkg.Info) { i.AwakeStartedAt = "" }), false},
		{"garbled start time fails open", seat("worker", func(i *sessionpkg.Info) { i.AwakeStartedAt = "soon" }), false},
		{"start time in the future fails open", seat("worker", func(i *sessionpkg.Info) { i.AwakeStartedAt = at(time.Hour) }), false},
		{"not a pool seat", seat("worker", func(i *sessionpkg.Info) { i.PoolManaged = false }), false},
		{"suspended pool", seat("paused", nil), false},
		{"pool gone from config", seat("removed", nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := poolSeatInSpawnGrace(tc.info, cfg, now); got != tc.want {
				t.Fatalf("poolSeatInSpawnGrace = %v, want %v", got, tc.want)
			}
		})
	}
}
