package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gcruntime "github.com/gastownhall/gascity/internal/runtime"
)

// mockFetcher implements StateFetcher for testing. calls counts list-panes
// fetches and procCalls counts ps scans. state.ProcessesAvailable=false makes
// every ps scan fail.
type mockFetcher struct {
	mu        sync.Mutex
	calls     int
	procCalls int
	sessions  map[string]bool
	state     runtimeStateSnapshot
	err       error
	delay     time.Duration
	procDelay time.Duration
}

func (m *mockFetcher) FetchSessions(ctx context.Context) (map[string]sessionRuntimeState, error) {
	m.mu.Lock()
	m.calls++
	state := m.state
	sessions := m.sessions
	err := m.err
	delay := m.delay
	m.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if state.Sessions == nil && sessions != nil {
		state.Sessions = make(map[string]sessionRuntimeState, len(sessions))
		for name, running := range sessions {
			state.Sessions[name] = sessionRuntimeState{Running: running}
		}
	}
	return state.Sessions, err
}

func (m *mockFetcher) FetchProcesses(ctx context.Context) (processSnapshot, error) {
	m.mu.Lock()
	m.procCalls++
	state := m.state
	delay := m.procDelay
	m.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return processSnapshot{}, ctx.Err()
		}
	}
	if !state.ProcessesAvailable {
		return processSnapshot{}, errors.New("mock: ps scan failed")
	}
	return state.Processes, nil
}

func (m *mockFetcher) getProcCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procCalls
}

func (m *mockFetcher) setState(state runtimeStateSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = state
}

func (m *mockFetcher) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *mockFetcher) setResult(sessions map[string]bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = sessions
	m.state = runtimeStateSnapshot{}
	m.err = err
}

type controlledRefreshFetcher struct {
	mu        sync.Mutex
	calls     int
	state     runtimeStateSnapshot
	blockCall int
	entered   chan struct{}
	release   chan struct{}
}

func (f *controlledRefreshFetcher) FetchProcesses(context.Context) (processSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state.Processes, nil
}

func (f *controlledRefreshFetcher) FetchSessions(ctx context.Context) (map[string]sessionRuntimeState, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	state := f.state
	f.mu.Unlock()

	if call == f.blockCall {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return state.Sessions, nil
}

func (f *controlledRefreshFetcher) getCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestStateCache_FreshCacheReturnsCorrectState(t *testing.T) {
	f := &mockFetcher{
		sessions: map[string]bool{"agent-1": true, "agent-2": true},
	}
	cache := NewStateCache(f, 2*time.Second)

	if !cache.IsRunning("agent-1") {
		t.Error("expected agent-1 to be running")
	}
	if !cache.IsRunning("agent-2") {
		t.Error("expected agent-2 to be running")
	}
	if cache.IsRunning("agent-3") {
		t.Error("expected agent-3 to not be running")
	}

	// Only one fetch should have occurred (the first call populated the cache,
	// the subsequent calls should use the cached data).
	if got := f.getCalls(); got != 1 {
		t.Errorf("expected 1 fetch call, got %d", got)
	}
}

func TestStateCache_StaleCacheTriggersRefresh(t *testing.T) {
	f := &mockFetcher{
		sessions: map[string]bool{"agent-1": true},
	}
	ttl := 50 * time.Millisecond
	cache := NewStateCache(f, ttl)

	// Prime the cache.
	if !cache.IsRunning("agent-1") {
		t.Fatal("expected agent-1 to be running initially")
	}
	if got := f.getCalls(); got != 1 {
		t.Fatalf("expected 1 fetch call after prime, got %d", got)
	}

	// Update the fetcher result and wait for the cache to go stale.
	f.setResult(map[string]bool{"agent-1": true, "agent-2": true}, nil)
	time.Sleep(ttl + 10*time.Millisecond)

	// This call should trigger a refresh.
	if !cache.IsRunning("agent-2") {
		t.Error("expected agent-2 to be running after stale refresh")
	}
	if got := f.getCalls(); got != 2 {
		t.Errorf("expected 2 fetch calls after stale, got %d", got)
	}
}

func TestStateCache_ConcurrentCallersCoalesceIntoOneFetch(t *testing.T) {
	f := &mockFetcher{
		sessions: map[string]bool{"agent-1": true},
		delay:    100 * time.Millisecond,
	}
	cache := NewStateCache(f, 2*time.Second)

	var wg sync.WaitGroup
	results := make([]bool, 20)
	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = cache.IsRunning("agent-1")
		}(i)
	}
	wg.Wait()

	// All should have gotten the correct result.
	for i, r := range results {
		if !r {
			t.Errorf("goroutine %d: expected true, got false", i)
		}
	}

	// singleflight should have coalesced all callers into exactly 1 fetch.
	if got := f.getCalls(); got != 1 {
		t.Errorf("expected 1 fetch call (singleflight), got %d", got)
	}
}

func TestStateCache_ProcessAliveUsesFreshSnapshot(t *testing.T) {
	f := &mockFetcher{
		state: runtimeStateSnapshot{
			Sessions: map[string]sessionRuntimeState{
				"agent-1": {
					Running: true,
					Panes: []paneRuntimeState{{
						Command: "claude",
						PID:     "101",
					}},
				},
			},
			Processes: newProcessSnapshot([]processRuntimeState{{
				PID:     "101",
				PPID:    "1",
				Command: "claude",
				Args:    "claude --dangerously-skip-permissions",
			}}),
			ProcessesAvailable: true,
		},
	}
	cache := NewStateCache(f, 2*time.Second)

	if !cache.ProcessAlive("agent-1", []string{"claude"}) {
		t.Fatal("ProcessAlive(agent-1, claude) = false, want true")
	}
	if !cache.IsRunning("agent-1") {
		t.Fatal("IsRunning(agent-1) = false, want true from same snapshot")
	}
	if cache.ProcessAlive("agent-1", []string{"codex"}) {
		t.Fatal("ProcessAlive(agent-1, codex) = true, want false")
	}
	if got := f.getCalls(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1 across ProcessAlive and IsRunning", got)
	}
}

// TestStateCache_DegradedProcessSnapshotRetainsLiveness asserts the control
// plane stays correct when the OS process-table snapshot is unavailable (the
// ps scan lost the CPU race to a busy fleet). tmux already proved the session
// is alive, so IsRunning must stay true and ProcessAlive must degrade
// optimistically (NOT report the process dead, which would wrongly reap it).
func TestStateCache_DegradedProcessSnapshotRetainsLiveness(t *testing.T) {
	f := &mockFetcher{
		state: runtimeStateSnapshot{
			Sessions: map[string]sessionRuntimeState{
				"agent-1": {
					Running: true,
					Panes:   []paneRuntimeState{{Command: "bash", PID: "101"}},
				},
			},
			// Processes empty + ProcessesAvailable=false models a failed ps scan.
			ProcessesAvailable: false,
		},
	}
	cache := NewStateCache(f, 2*time.Second)

	if !cache.IsRunning("agent-1") {
		t.Fatal("IsRunning(agent-1) = false under degraded snapshot, want true (tmux liveness retained)")
	}
	if !cache.ProcessAlive("agent-1", []string{"claude"}) {
		t.Fatal("ProcessAlive(agent-1, claude) = false under degraded snapshot, want true (optimistic degrade, never report dead)")
	}
	if cache.IsRunning("agent-2") {
		t.Fatal("IsRunning(agent-2) = true, want false for an absent session even when degraded")
	}
}

// shellPaneWithClaude is one session, agent-1, whose bash pane (PID 101) runs
// claude as a child. It is the fixture for the ps-off-the-caller-path tests.
func shellPaneWithClaude() runtimeStateSnapshot {
	return runtimeStateSnapshot{
		Sessions: map[string]sessionRuntimeState{
			"agent-1": {Running: true, Panes: []paneRuntimeState{{Command: "bash", PID: "101"}}},
		},
		Processes: newProcessSnapshot([]processRuntimeState{
			{PID: "101", PPID: "1", Command: "bash", Args: "bash -lc claude"},
			{PID: "102", PPID: "101", Command: "claude", Args: "claude"},
		}),
		ProcessesAvailable: true,
	}
}

// waitFor polls cond for up to 5s. The ps scans below run in the background,
// so a test can only watch for their effect.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// vn-cj72bmr: a refresh used to run tmux list-panes AND two full ps scans
// under one 3s timeout, so under load every IsRunning past the 2s TTL waited
// on ps. IsRunning only needs list-panes, so it must never start a ps scan.
func TestStateCache_IsRunningNeverRunsPS(t *testing.T) {
	f := &mockFetcher{state: shellPaneWithClaude()}
	cache := NewStateCache(f, time.Nanosecond) // every read refreshes

	for range 5 {
		if !cache.IsRunning("agent-1") {
			t.Fatal("IsRunning(agent-1) = false, want true")
		}
	}
	if got := f.getCalls(); got < 2 {
		t.Fatalf("list-panes fetches = %d, want the expired TTL to refresh on each read", got)
	}
	if got := f.getProcCalls(); got != 0 {
		t.Fatalf("ps scans = %d after only IsRunning reads, want 0", got)
	}
}

// Once a scan has landed, a slow ps must not hold up ProcessAlive: it answers
// from the last good snapshot while the new scan runs in the background.
func TestStateCache_ProcessAliveDoesNotWaitOnSlowPS(t *testing.T) {
	f := &mockFetcher{state: shellPaneWithClaude()}
	cache := NewStateCache(f, time.Hour)

	if !cache.ProcessAlive("agent-1", []string{"claude"}) {
		t.Fatal("first ProcessAlive = false, want true from the first scan")
	}
	if got := f.getProcCalls(); got != 1 {
		t.Fatalf("ps scans = %d after the first ProcessAlive, want 1 (it waits for the first scan)", got)
	}

	f.mu.Lock()
	f.procDelay = 2 * time.Second
	f.mu.Unlock()
	cache.mu.Lock()
	cache.procsTTL = time.Nanosecond // the snapshot is always due for a rescan
	cache.mu.Unlock()

	start := time.Now()
	alive := cache.ProcessAlive("agent-1", []string{"claude"})
	elapsed := time.Since(start)
	if !alive {
		t.Fatal("ProcessAlive = false, want true from the last good snapshot")
	}
	if elapsed > time.Second {
		t.Fatalf("ProcessAlive took %v with a 2s ps scan running, want it to return without waiting", elapsed)
	}
	waitFor(t, "the background ps scan to start", func() bool { return f.getProcCalls() == 2 })
}

// A pane that appeared after the last scan began (a new session, a respawned
// pane, or one another gc process started) is not in that scan. The scan must
// not call it dead: that would read as a crashed agent. Once a scan that began
// after the pane landed, its answer is trusted again, including a "dead".
func TestStateCache_ScanOlderThanPaneCannotCallItDead(t *testing.T) {
	f := &mockFetcher{state: shellPaneWithClaude()}
	cache := NewStateCache(f, time.Hour)
	if !cache.ProcessAlive("agent-1", []string{"claude"}) {
		t.Fatal("ProcessAlive(agent-1) = false, want true")
	}

	// agent-2 appears with a bash pane (PID 201) that has no claude child. The
	// next scan is slow, so the old snapshot (no PID 201 at all) is what the
	// read sees.
	withNewPane := shellPaneWithClaude()
	withNewPane.Sessions["agent-2"] = sessionRuntimeState{Running: true, Panes: []paneRuntimeState{{Command: "bash", PID: "201"}}}
	withNewPane.Processes = newProcessSnapshot([]processRuntimeState{
		{PID: "101", PPID: "1", Command: "bash", Args: "bash -lc claude"},
		{PID: "102", PPID: "101", Command: "claude", Args: "claude"},
		{PID: "201", PPID: "1", Command: "bash", Args: "bash"},
	})
	f.setState(withNewPane)
	f.mu.Lock()
	f.procDelay = 200 * time.Millisecond
	f.mu.Unlock()
	cache.Invalidate()

	if !cache.ProcessAlive("agent-2", []string{"claude"}) {
		t.Fatal("ProcessAlive(agent-2) = false from a scan that began before its pane existed, want the optimistic true")
	}
	if !cache.ProcessAlive("agent-1", []string{"claude"}) {
		t.Fatal("ProcessAlive(agent-1) = false, want true: its pane is older than the scan")
	}

	// The read above started a fresh scan. When it lands it began after the
	// pane was seen, so its "no claude under PID 201" is believed.
	waitFor(t, "a scan newer than agent-2's pane to report it dead", func() bool {
		return !cache.ProcessAlive("agent-2", []string{"claude"})
	})
}

// A failed scan keeps the last good snapshot, so a real "dead" still reads as
// dead. Past the staleTTL cliff the snapshot is dropped and ProcessAlive
// degrades optimistically, the same as a cache that never had one.
func TestStateCache_FailedPSKeepsLastGoodUntilStaleTTL(t *testing.T) {
	f := &mockFetcher{state: shellPaneWithClaude()}
	cache := NewStateCache(f, time.Hour)
	if cache.ProcessAlive("agent-1", []string{"codex"}) {
		t.Fatal("ProcessAlive(agent-1, codex) = true, want false from a good scan")
	}

	failing := shellPaneWithClaude()
	failing.ProcessesAvailable = false
	f.setState(failing)
	cache.mu.Lock()
	cache.procsTTL = time.Nanosecond
	cache.mu.Unlock()

	if cache.ProcessAlive("agent-1", []string{"codex"}) {
		t.Fatal("ProcessAlive(agent-1, codex) = true while ps fails, want false from the last good snapshot")
	}
	waitFor(t, "the failing background scan", func() bool { return f.getProcCalls() >= 2 })

	// Only the process snapshot passes the cliff: list-panes stays fresh.
	// The pane is aged too, so the scan still postdates it and only the cliff
	// can explain an optimistic answer.
	cache.mu.Lock()
	cache.procs.startedAt = time.Now().Add(-2 * cache.staleTTL)
	cache.state.Sessions["agent-1"].Panes[0].FirstSeen = cache.procs.startedAt.Add(-time.Second)
	cache.mu.Unlock()
	if !cache.ProcessAlive("agent-1", []string{"codex"}) {
		t.Fatal("ProcessAlive(agent-1, codex) = false past staleTTL, want the optimistic true")
	}
	if !cache.IsRunning("agent-1") {
		t.Fatal("IsRunning(agent-1) = false, want true: tmux liveness does not depend on ps")
	}
}

func TestStateCache_ProcessAliveMatchesShellDescendantFromSnapshot(t *testing.T) {
	f := &mockFetcher{
		state: runtimeStateSnapshot{
			Sessions: map[string]sessionRuntimeState{
				"agent-1": {
					Running: true,
					Panes: []paneRuntimeState{{
						Command: "bash",
						PID:     "101",
					}},
				},
			},
			Processes: newProcessSnapshot([]processRuntimeState{
				{PID: "101", PPID: "1", Command: "bash", Args: "bash -lc codex"},
				{PID: "102", PPID: "101", Command: "node", Args: "node /usr/local/bin/codex"},
			}),
			ProcessesAvailable: true,
		},
	}
	cache := NewStateCache(f, 2*time.Second)

	if !cache.ProcessAlive("agent-1", []string{"codex"}) {
		t.Fatal("ProcessAlive(agent-1, codex) = false, want true from cached descendant snapshot")
	}
	if got := f.getCalls(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
}

func TestProviderObserveLivenessUsesCacheProcessSnapshot(t *testing.T) {
	f := &mockFetcher{
		state: runtimeStateSnapshot{
			Sessions: map[string]sessionRuntimeState{
				"agent-1": {
					Running: true,
					Panes: []paneRuntimeState{{
						Command: "bash",
						PID:     "101",
					}},
				},
			},
			Processes: newProcessSnapshot([]processRuntimeState{
				{PID: "101", PPID: "1", Command: "bash", Args: "bash -lc codex"},
				{PID: "102", PPID: "101", Command: "node", Args: "node /usr/local/bin/codex"},
			}),
			ProcessesAvailable: true,
		},
	}
	provider := &Provider{cache: NewStateCache(f, time.Hour)}

	got := provider.ObserveLiveness("agent-1", []string{"codex"})
	if !got.Running || !got.Alive {
		t.Fatalf("ObserveLiveness = %+v, want running and alive from cache", got)
	}
	got = provider.ObserveLiveness("agent-1", []string{"codex"})
	if !got.Running || !got.Alive {
		t.Fatalf("second ObserveLiveness = %+v, want running and alive from cache", got)
	}
	if calls := f.getCalls(); calls != 1 {
		t.Fatalf("fetch calls = %d, want 1 across repeated ObserveLiveness calls", calls)
	}
}

// FetchSessions must report an unreachable tmux server as an observation FAILURE
// (runtime.ErrRuntimeUnavailable), not as an empty success. The empty-success
// form let refresh() overwrite last-known-good and instantly report every
// session not-running, draining healthy pool slots on a brief tmux blip. The
// wrapped error must still satisfy isNoServerError so downstream absorbers keep
// working.
func TestTmuxFetcher_NoServerMapsToRuntimeUnavailable(t *testing.T) {
	f := &tmuxFetcher{tm: &Tmux{cfg: DefaultConfig(), exec: &fakeExecutor{err: ErrNoServer}}}

	sessions, err := f.FetchSessions(context.Background())
	if err == nil {
		t.Fatalf("FetchSessions() err = nil (sessions %+v), want an error for an unreachable server", sessions)
	}
	if !errors.Is(err, gcruntime.ErrRuntimeUnavailable) {
		t.Fatalf("FetchSessions() err = %v, want errors.Is(runtime.ErrRuntimeUnavailable)", err)
	}
	if !isNoServerError(err) {
		t.Fatalf("FetchSessions() err = %v must still satisfy isNoServerError so downstream ErrNoServer absorbers work", err)
	}
}

// End to end at the cache: after a good prime, an ErrNoServer refresh must
// preserve last-known-good (within staleTTL) instead of collapsing to empty.
func TestStateCache_NoServerRefreshPreservesLastKnownGood(t *testing.T) {
	fe := &fakeExecutor{
		// FetchSessions issues exactly one executor call (list-panes). First
		// call primes one live pane, every later call reports no server.
		outs: []string{"agent-1\t0\tclaude\t123"},
		errs: []error{nil, ErrNoServer, ErrNoServer, ErrNoServer},
	}
	// TTL 0 forces every read to refresh unconditionally (time.Since(fetchedAt)
	// is never < 0). A nanosecond TTL is non-deterministic here: on a coarse
	// monotonic clock time.Since can read 0 on the very next call, so the second
	// IsRunning may skip the refresh and leave lastError nil (flaky).
	cache := NewStateCache(&tmuxFetcher{tm: &Tmux{cfg: DefaultConfig(), exec: fe}}, 0)

	if !cache.IsRunning("agent-1") {
		t.Fatal("expected agent-1 running after prime")
	}
	// TTL 0, so the next read forces a refresh that hits ErrNoServer.
	// Last-known-good must survive it (staleTTL default 30s).
	if !cache.IsRunning("agent-1") {
		t.Error("expected agent-1 still running after an ErrNoServer refresh (last-known-good); a brief tmux outage must not report sessions as gone")
	}
	cache.mu.RLock()
	lastErr := cache.lastError
	cache.mu.RUnlock()
	if !errors.Is(lastErr, gcruntime.ErrRuntimeUnavailable) {
		t.Fatalf("cache.lastError = %v, want errors.Is(runtime.ErrRuntimeUnavailable)", lastErr)
	}
}

// An UNPRIMED cache (never held a good state, fetchedAt zero) that hits a
// genuine "no server" must prime itself to an empty snapshot rather than
// re-spawning list-panes and re-logging the failure on every IsRunning. A
// fresh city with no tmux server yet would otherwise storm the (absent) server
// with one list-panes per liveness probe.
func TestStateCache_UnprimedNoServerPrimesEmptyWithoutRefetch(t *testing.T) {
	fe := &fakeExecutor{
		// Every list-panes reports no server; the cache is never primed good.
		errs: []error{ErrNoServer, ErrNoServer, ErrNoServer, ErrNoServer},
	}
	// A real TTL (not 0) so a successfully primed empty snapshot is a cache hit
	// on the next read — proving priming stops the refetch storm.
	cache := NewStateCache(&tmuxFetcher{tm: &Tmux{cfg: DefaultConfig(), exec: fe}}, time.Second)

	if cache.IsRunning("agent-1") {
		t.Fatal("expected agent-1 not running against a server-less city")
	}
	// The first read primed an empty snapshot with a single list-panes spawn.
	// Every subsequent read within the TTL must be a cache hit — no refetch.
	_ = cache.IsRunning("agent-1")
	_ = cache.IsRunning("agent-2")
	if calls := len(fe.calls); calls != 1 {
		t.Fatalf("list-panes calls = %d, want 1: an unprimed no-server must prime empty once, not refetch on every IsRunning", calls)
	}

	// The cache is primed: fetchedAt set, and the failure recorded in lastError.
	cache.mu.RLock()
	fetchedAt := cache.fetchedAt
	lastErr := cache.lastError
	cache.mu.RUnlock()
	if fetchedAt.IsZero() {
		t.Error("expected fetchedAt to be set (cache primed) after an unprimed no-server refresh")
	}
	if !errors.Is(lastErr, gcruntime.ErrRuntimeUnavailable) {
		t.Errorf("cache.lastError = %v, want errors.Is(runtime.ErrRuntimeUnavailable)", lastErr)
	}
}

func TestStateCache_RefreshFailurePreservesLastKnownGood(t *testing.T) {
	f := &mockFetcher{
		sessions: map[string]bool{"agent-1": true},
	}
	ttl := 50 * time.Millisecond
	cache := NewStateCache(f, ttl)

	// Prime the cache.
	if !cache.IsRunning("agent-1") {
		t.Fatal("expected agent-1 running initially")
	}

	// Make the fetcher fail and wait for staleness.
	f.setResult(nil, errors.New("tmux subprocess failed"))
	time.Sleep(ttl + 10*time.Millisecond)

	// The cache should still report the last-known-good state.
	if !cache.IsRunning("agent-1") {
		t.Error("expected agent-1 still running after refresh failure (last-known-good)")
	}

	// Verify the error is recorded.
	cache.mu.RLock()
	lastErr := cache.lastError
	cache.mu.RUnlock()
	if lastErr == nil {
		t.Error("expected lastError to be set after refresh failure")
	}
}

func TestStateCache_DiscardRefreshAfterEvictSession(t *testing.T) {
	state := runtimeStateSnapshot{
		Sessions: map[string]sessionRuntimeState{
			"agent-1": {Running: true},
		},
	}
	f := &controlledRefreshFetcher{
		state:     state,
		blockCall: 2,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	cache := NewStateCache(f, time.Nanosecond)

	if !cache.IsRunning("agent-1") {
		t.Fatal("expected agent-1 running after prime")
	}
	time.Sleep(time.Millisecond)

	result := make(chan bool, 1)
	go func() {
		result <- cache.IsRunning("agent-1")
	}()

	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refresh to start")
	}
	cache.EvictSession("agent-1")
	close(f.release)

	select {
	case got := <-result:
		if got {
			t.Fatal("IsRunning(agent-1) = true after concurrent eviction, want false")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for IsRunning result")
	}
	if calls := f.getCalls(); calls != 2 {
		t.Fatalf("fetch calls = %d, want 2", calls)
	}
}

func TestStateCache_InvalidateForcesNextReadToRefresh(t *testing.T) {
	f := &mockFetcher{
		sessions: map[string]bool{"agent-1": true},
	}
	cache := NewStateCache(f, 10*time.Second) // long TTL

	// Prime the cache.
	if !cache.IsRunning("agent-1") {
		t.Fatal("expected agent-1 running initially")
	}
	if got := f.getCalls(); got != 1 {
		t.Fatalf("expected 1 fetch call, got %d", got)
	}

	// Update fetcher result and invalidate.
	f.setResult(map[string]bool{"agent-2": true}, nil)
	cache.Invalidate()

	// The next read should trigger a fresh fetch.
	if cache.IsRunning("agent-1") {
		t.Error("expected agent-1 to not be running after invalidate + new fetch")
	}
	if !cache.IsRunning("agent-2") {
		t.Error("expected agent-2 to be running after invalidate + new fetch")
	}
	if got := f.getCalls(); got != 2 {
		t.Errorf("expected 2 fetch calls after invalidate, got %d", got)
	}
}

func TestStateCache_StaleTTLReturnsFalseForAllSessions(t *testing.T) {
	f := &mockFetcher{
		sessions: map[string]bool{"agent-1": true},
	}
	ttl := 50 * time.Millisecond
	cache := NewStateCache(f, ttl)
	cache.staleTTL = 100 * time.Millisecond // short staleTTL for testing

	// Prime the cache.
	if !cache.IsRunning("agent-1") {
		t.Fatal("expected agent-1 running initially")
	}

	// Make all subsequent fetches fail.
	f.setResult(nil, errors.New("tmux dead"))

	// Wait past staleTTL.
	time.Sleep(150 * time.Millisecond)

	// After staleTTL, the cache should return false for everything.
	if cache.IsRunning("agent-1") {
		t.Error("expected agent-1 to be reported as not running after staleTTL exceeded")
	}
}

func TestStateCache_EmptySessionsMap(t *testing.T) {
	f := &mockFetcher{
		sessions: map[string]bool{},
	}
	cache := NewStateCache(f, 2*time.Second)

	if cache.IsRunning("anything") {
		t.Error("expected false for any session when tmux has no sessions")
	}
}

func TestFetchProcessSnapshotCanceledContextReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fetchProcessSnapshot(ctx)
	if err == nil {
		t.Fatal("fetchProcessSnapshot canceled context returned nil error")
	}
}

func TestParseProcessSnapshotLineFixedColumns(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantPID  string
		wantPPID string
		wantCmd  string
		wantArgs string
	}{
		{
			name:     "typical line",
			line:     fmt.Sprintf("%10s %10s %-64s %s", "123", "1", "claude code", "claude code --print"),
			wantPID:  "123",
			wantPPID: "1",
			wantCmd:  "claude code",
			wantArgs: "claude code --print",
		},
		{
			// Linux comm can also contain internal whitespace —
			// applications can set thread names via prctl(PR_SET_NAME)
			// to strings like "Renderer Main" or "I/O Worker". The
			// fixed-column slice preserves them; a whitespace
			// tokenizer would not.
			name:     "comm with internal spaces (PR_SET_NAME style)",
			line:     fmt.Sprintf("%10s %10s %-64s %s", "456", "1", "Renderer Main", "/opt/app/bin/renderer --headless"),
			wantPID:  "456",
			wantPPID: "1",
			wantCmd:  "Renderer Main",
			wantArgs: "/opt/app/bin/renderer --headless",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcessSnapshotLineFixedColumns(tc.line)
			if !ok {
				t.Fatalf("returned ok=false for %q", tc.line)
			}
			if got.PID != tc.wantPID {
				t.Errorf("PID = %q, want %q", got.PID, tc.wantPID)
			}
			if got.PPID != tc.wantPPID {
				t.Errorf("PPID = %q, want %q", got.PPID, tc.wantPPID)
			}
			if got.Command != tc.wantCmd {
				t.Errorf("Command = %q, want %q", got.Command, tc.wantCmd)
			}
			if got.Args != tc.wantArgs {
				t.Errorf("Args = %q, want %q", got.Args, tc.wantArgs)
			}
		})
	}
}

func TestParseProcessSnapshotLineDarwin(t *testing.T) {
	// macOS omits fixed-width modifiers because its ps rejects Linux's
	// `:N=` syntax. The Darwin snapshot asks for pid, ppid, and args only;
	// the command name is derived from argv[0] instead of guessing where an
	// unbounded comm column ends.
	cases := []struct {
		name     string
		line     string
		wantPID  string
		wantPPID string
		wantCmd  string
		wantArgs string
	}{
		{
			name:     "short argv0 with dynamic numeric columns",
			line:     "  123     1 /sbin/launchd --boot",
			wantPID:  "123",
			wantPPID: "1",
			wantCmd:  "launchd",
			wantArgs: "/sbin/launchd --boot",
		},
		{
			name:     "dynamic columns with no comm width dependency",
			line:     "  489     1 /usr/sbin/coreaudiod Core Audio Driver (MSTeamsAudioDevice.driver)",
			wantPID:  "489",
			wantPPID: "1",
			wantCmd:  "coreaudiod",
			wantArgs: "/usr/sbin/coreaudiod Core Audio Driver (MSTeamsAudioDevice.driver)",
		},
		{
			name:     "wide pid and ppid columns",
			line:     "123456 98765 /opt/app/bin/renderer --headless",
			wantPID:  "123456",
			wantPPID: "98765",
			wantCmd:  "renderer",
			wantArgs: "/opt/app/bin/renderer --headless",
		},
		{
			name:     "args preserves internal multi-space",
			line:     "  456     1 /usr/local/bin/claude a  b   c",
			wantPID:  "456",
			wantPPID: "1",
			wantCmd:  "claude",
			wantArgs: "/usr/local/bin/claude a  b   c",
		},
		{
			name:     "outer args whitespace is normalized",
			line:     "  789     1    /bin/zsh -l   ",
			wantPID:  "789",
			wantPPID: "1",
			wantCmd:  "zsh",
			wantArgs: "/bin/zsh -l",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcessSnapshotLineDarwin(tc.line)
			if !ok {
				t.Fatalf("returned ok=false for %q", tc.line)
			}
			if got.PID != tc.wantPID {
				t.Errorf("PID = %q, want %q", got.PID, tc.wantPID)
			}
			if got.PPID != tc.wantPPID {
				t.Errorf("PPID = %q, want %q", got.PPID, tc.wantPPID)
			}
			if got.Command != tc.wantCmd {
				t.Errorf("Command = %q, want %q", got.Command, tc.wantCmd)
			}
			if got.Args != tc.wantArgs {
				t.Errorf("Args = %q, want %q", got.Args, tc.wantArgs)
			}
		})
	}
}

func TestParseProcessSnapshotLineDarwinRejectsMalformed(t *testing.T) {
	// Inputs the parser must NOT accept — empty, too few columns to
	// extract pid/ppid/comm. Returning ok=false lets the caller skip
	// the row instead of recording garbage.
	cases := []struct {
		name string
		line string
	}{
		{"empty line", ""},
		{"whitespace only", "       "},
		{"pid only", "  123"},
		{"pid and ppid only", "  123     1"},
		{"pid, ppid, separator, no args", "  123     1 "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseProcessSnapshotLineDarwin(tc.line); ok {
				t.Errorf("expected ok=false for %q", tc.line)
			}
		})
	}
}

func TestParseDarwinProcessSnapshotPreservesCommWhenArgv0IsRewritten(t *testing.T) {
	argsOut := strings.Join([]string{
		"  101     1 /bin/zsh -l",
		"  102   101 2.1.30 --print",
	}, "\n")
	commOut := strings.Join([]string{
		"  101     1 zsh",
		"  102   101 /usr/local/bin/claude",
	}, "\n")

	snapshot := parseDarwinProcessSnapshot(argsOut, commOut)
	process, ok := snapshot.byPID["102"]
	if !ok {
		t.Fatal("process 102 missing from Darwin snapshot")
	}
	if process.Command != "/usr/local/bin/claude" {
		t.Fatalf("Command = %q, want comm path", process.Command)
	}
	if process.Args != "2.1.30 --print" {
		t.Fatalf("Args = %q, want rewritten argv[0] args", process.Args)
	}
	if !snapshot.processMatchesNames("102", processNameSet([]string{"claude"})) {
		t.Fatal("processMatchesNames(102, claude) = false, want true from comm")
	}
}

func TestParseDarwinProcessSnapshotTraversesThroughEmptyArgsRows(t *testing.T) {
	argsOut := strings.Join([]string{
		"  101     1 /bin/bash -l",
		"  102   101 ",
		"  103   102 /usr/local/bin/claude --print",
	}, "\n")
	commOut := strings.Join([]string{
		"  101     1 bash",
		"  102   101 launchd helper",
		"  103   102 claude",
	}, "\n")

	snapshot := parseDarwinProcessSnapshot(argsOut, commOut)
	process, ok := snapshot.byPID["102"]
	if !ok {
		t.Fatal("process 102 missing from Darwin snapshot")
	}
	if process.Command != "launchd helper" {
		t.Fatalf("Command = %q, want comm for empty-args process", process.Command)
	}
	if process.Args != "" {
		t.Fatalf("Args = %q, want empty args", process.Args)
	}
	if !snapshot.hasDescendantWithNames("101", processNameSet([]string{"claude"}), 0) {
		t.Fatal("hasDescendantWithNames(101, claude) = false, want traversal through empty-args row")
	}
}

func TestProcessSnapshotPSArgsRejectsLinuxSyntaxOnDarwin(t *testing.T) {
	// Regression: macOS ps rejects the BSD `:N=` column-width form. Confirm
	// we don't emit it on Darwin. Skip elsewhere — Linux ps accepts both
	// forms so verifying the wide form there is just a tautology.
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin-specific syntax guard")
	}
	args := processSnapshotPSArgs()
	for _, a := range args {
		if strings.Contains(a, ":") {
			t.Fatalf("processSnapshotPSArgs returned %v on darwin; contains Linux-only `:N=` width specifier", args)
		}
	}
}

func TestStateCache_NilSessionsMap(t *testing.T) {
	// FetchRunning returns nil map (e.g., no tmux server) — same as empty.
	f := &mockFetcher{
		sessions: nil,
	}
	cache := NewStateCache(f, 2*time.Second)

	if cache.IsRunning("anything") {
		t.Error("expected false for any session when fetch returns nil map")
	}
}

func TestStateCache_ConcurrentInvalidateAndRead(_ *testing.T) {
	var fetchCount atomic.Int64
	f := &mockFetcher{
		sessions: map[string]bool{"agent-1": true},
	}

	cache := NewStateCache(f, 50*time.Millisecond)

	// Prime.
	cache.IsRunning("agent-1")

	var wg sync.WaitGroup
	// Hammer with concurrent reads and invalidates.
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			cache.IsRunning("agent-1")
			_ = fetchCount.Load()
		}()
		go func() {
			defer wg.Done()
			cache.Invalidate()
		}()
	}
	wg.Wait()

	// No panics, no data races — that's the assertion (run with -race).
}

// TestStateCache_RefreshLogIsOptInViaEnvVar verifies that the successful
// refresh log line is silent by default and only emitted when
// GC_LOG_TMUX_CACHE=true. Regression test for #644.
func TestStateCache_RefreshLogIsOptInViaEnvVar(t *testing.T) {
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	t.Run("silent by default", func(t *testing.T) {
		buf.Reset()
		t.Setenv("GC_LOG_TMUX_CACHE", "")

		f := &mockFetcher{sessions: map[string]bool{"a": true}}
		cache := NewStateCache(f, 50*time.Millisecond)
		cache.IsRunning("a")

		if got := buf.String(); got != "" {
			t.Errorf("expected no log output by default, got %q", got)
		}
	})

	t.Run("logs when opted in", func(t *testing.T) {
		buf.Reset()
		t.Setenv("GC_LOG_TMUX_CACHE", "true")

		f := &mockFetcher{sessions: map[string]bool{"a": true}}
		cache := NewStateCache(f, 50*time.Millisecond)
		cache.IsRunning("a")

		got := buf.String()
		if !strings.Contains(got, "tmux state cache: refreshed") {
			t.Errorf("expected refresh log with GC_LOG_TMUX_CACHE=true, got %q", got)
		}
		if strings.Contains(got, "refresh failed") {
			t.Errorf("unexpected failure log in success path, got %q", got)
		}
	})

	t.Run("failure log still emitted when opt-out", func(t *testing.T) {
		buf.Reset()
		t.Setenv("GC_LOG_TMUX_CACHE", "")

		f := &mockFetcher{err: errors.New("boom")}
		cache := NewStateCache(f, 50*time.Millisecond)
		cache.IsRunning("a")

		got := buf.String()
		if !strings.Contains(got, "tmux state cache: refresh failed") {
			t.Errorf("expected refresh-failed log regardless of GC_LOG_TMUX_CACHE, got %q", got)
		}
	})
}

func TestIsNoServerErrorRecognizesSentinel(t *testing.T) {
	if !isNoServerError(ErrNoServer) {
		t.Fatal("isNoServerError(ErrNoServer) = false, want true")
	}
}

// TestProcessAliveWrappedPane pins the wrapped-pane liveness contract
// (GC_AGENT_SLICE): a pane whose root command is systemd-run — not an agent
// name, a shell, or a known interpreter — still counts as alive when the
// agent runs as its descendant, via processAlive's unconditional descendant
// fallback.
func TestProcessAliveWrappedPane(t *testing.T) {
	snapshot := newProcessSnapshot([]processRuntimeState{
		{PID: "100", PPID: "1", Command: "systemd-run", Args: "systemd-run --user --scope --slice=gascity-agents.slice --collect --quiet -- sh -c claude"},
		{PID: "101", PPID: "100", Command: "claude", Args: "claude"},
	})
	pane := paneRuntimeState{Command: "systemd-run", PID: "100"}
	if !pane.processAlive(processNameSet([]string{"claude"}), snapshot) {
		t.Fatal("processAlive = false for systemd-run pane with claude child, want true (descendant fallback)")
	}
}
