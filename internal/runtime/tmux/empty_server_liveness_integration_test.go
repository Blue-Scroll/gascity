//go:build integration

package tmux

import (
	"context"
	"errors"
	"testing"

	gcruntime "github.com/gastownhall/gascity/internal/runtime"
)

// The ga-jnavd production shape, against a real tmux server: a city server
// configured with `exit-empty off` outlives its last session, and `list-panes
// -a` then exits non-zero with "no current target". The state cache must read
// that as an empty fleet, not as a runtime outage.
func TestStateCache_RealEmptyServerObservesEmptyFleet(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not installed")
	}

	cfg := DefaultConfig()
	cfg.SocketName = testSocketName + "-empty"
	tm := NewTmuxWithConfig(cfg)
	t.Cleanup(func() { _ = tm.TeardownServer() })

	const session = "gc-test-empty-server"
	if err := tm.NewSessionWithCommand(session, t.TempDir(), "sleep 300"); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	// exit-empty off is what keeps the server alive with zero sessions; without
	// it the server exits and the failure degenerates into plain ErrNoServer.
	if err := tm.ConfigureServer(); err != nil {
		t.Fatalf("ConfigureServer: %v", err)
	}

	fetcher := &tmuxFetcher{tm: tm}
	sessions, err := fetcher.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions with one live session: %v", err)
	}
	if !sessions[session].Running {
		t.Fatalf("FetchSessions did not see the live session; sessions = %v", sessions)
	}

	if err := tm.KillSession(session); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	// kill-session completes on the server before the client returns, so the
	// very next observation must already see the empty-but-alive server.
	sessions, err = fetcher.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions against an alive empty server: err = %v (ErrRuntimeUnavailable=%t), want a successful empty observation",
			err, errors.Is(err, gcruntime.ErrRuntimeUnavailable))
	}
	if sessions == nil {
		t.Fatal("FetchSessions Sessions = nil for an alive empty server, want an empty non-nil map")
	}
	if len(sessions) != 0 {
		t.Fatalf("FetchSessions Sessions = %v after the last session was killed, want empty", sessions)
	}

	// Recovery: a new session on the same server must be observed again with no
	// restart and no cache reset.
	if err := tm.NewSessionWithCommand(session, t.TempDir(), "sleep 300"); err != nil {
		t.Fatalf("NewSessionWithCommand after empty: %v", err)
	}
	sessions, err = fetcher.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions after the server refilled: %v", err)
	}
	if !sessions[session].Running {
		t.Fatalf("session not observed after the server refilled; sessions = %v", sessions)
	}
}
