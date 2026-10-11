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
	"testing/synctest"
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
	err       error
	blockCall int
	entered   chan struct{}
	release   chan struct{}
}

// FetchProcesses never blocks: only list-panes is under the test's control.
// state.ProcessesAvailable=false makes the scan fail, as in mockFetcher.
func (f *controlledRefreshFetcher) FetchProcesses(context.Context) (processSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.state.ProcessesAvailable {
		return processSnapshot{}, errors.New("controlled: ps scan failed")
	}
	return f.state.Processes, nil
}

func (f *controlledRefreshFetcher) FetchSessions(ctx context.Context) (map[string]sessionRuntimeState, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	state := f.state
	err := f.err
	f.mu.Unlock()

	if call == f.blockCall {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return state.Sessions, err
}

func (f *controlledRefreshFetcher) getCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *controlledRefreshFetcher) setResult(state runtimeStateSnapshot, err error) {
	f.mu.Lock()
	f.state = state
	f.err = err
	f.mu.Unlock()
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
// claude as a child. It is the fixture for the ps-off-the-reader-path tests.
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

// A refresh used to run tmux list-panes AND two full ps scans under one 3s
// timeout, so every read past the 2s TTL waited on ps. Under load that wait
// was the full 3s about 50 times an hour, each one inside a reconciler tick
// (town vn-cj72bmr, re-applied by vn-sfvhllr). IsRunning only needs
// list-panes, so it must never start a ps scan.
func TestStateCache_IsRunningNeverRunsPS(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &mockFetcher{state: shellPaneWithClaude()}
		cache := NewStateCache(f, time.Millisecond)

		for range 5 {
			if !cache.IsRunning("agent-1") {
				t.Fatal("IsRunning(agent-1) = false, want true")
			}
			time.Sleep(2 * time.Millisecond) // past the TTL: the next read refreshes
		}
		synctest.Wait()
		if got := f.getCalls(); got != 5 {
			t.Fatalf("list-panes fetches = %d, want 5 (one per expired read)", got)
		}
		if got := f.getProcCalls(); got != 0 {
			t.Fatalf("ps scans = %d after only IsRunning reads, want 0", got)
		}
	})
}

// Once a scan has landed, a slow ps must not hold up ProcessAlive: it answers
// from the last good snapshot while the new scan runs in the background. On
// the bubble's fake clock any wait on the 2s scan would show as 2s elapsed.
func TestStateCache_ProcessAliveDoesNotWaitOnSlowPS(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		cache.procsTTL = time.Millisecond // the snapshot is due for a rescan
		cache.mu.Unlock()
		time.Sleep(2 * time.Millisecond)

		start := time.Now()
		alive := cache.ProcessAlive("agent-1", []string{"claude"})
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("ProcessAlive took %v with a 2s ps scan running, want no wait at all", elapsed)
		}
		if !alive {
			t.Fatal("ProcessAlive = false, want true from the last good snapshot")
		}
		synctest.Wait()
		if got := f.getProcCalls(); got != 2 {
			t.Fatalf("ps scans = %d, want the read to have started a background scan", got)
		}
		time.Sleep(3 * time.Second) // let the background scan finish inside the bubble
	})
}

// The first ProcessAlive of a cache's life is the one read that waits for a
// scan, so a one-shot CLI gets a real answer. It still waits no longer than
// fetchTimeout: a scan slower than that answers optimistically and lands for
// the next read.
func TestStateCache_FirstProcessAliveWaitsAtMostFetchTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &mockFetcher{state: shellPaneWithClaude(), procDelay: 5 * time.Second}
		cache := NewStateCache(f, time.Hour)

		start := time.Now()
		if !cache.ProcessAlive("agent-1", []string{"codex"}) {
			t.Fatal("ProcessAlive(codex) = false before any scan landed, want the optimistic true")
		}
		if elapsed := time.Since(start); elapsed != fetchTimeout {
			t.Fatalf("first ProcessAlive waited %v, want exactly fetchTimeout (%v)", elapsed, fetchTimeout)
		}

		time.Sleep(3 * time.Second) // the scan lands
		synctest.Wait()
		if cache.ProcessAlive("agent-1", []string{"codex"}) {
			t.Fatal("ProcessAlive(codex) = true after the scan landed, want false: no codex runs")
		}
		if got := f.getProcCalls(); got != 1 {
			t.Fatalf("ps scans = %d, want 1: the timed-out wait must not start a second scan", got)
		}
	})
}

// A pane that appeared after the last scan began (a new session, a respawned
// pane, or one another gc process started) is not in that scan. The scan must
// not call it dead: that would read as a crashed agent. Once a scan that began
// after the pane landed, its answer is trusted again, including a "dead".
func TestStateCache_ScanOlderThanPaneCannotCallItDead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &mockFetcher{state: shellPaneWithClaude()}
		cache := NewStateCache(f, time.Hour)
		if !cache.ProcessAlive("agent-1", []string{"claude"}) {
			t.Fatal("ProcessAlive(agent-1) = false, want true")
		}

		// agent-2 appears with a bash pane (PID 201) that has no claude child.
		// The next scan is slow, so the old snapshot (no PID 201 at all) is
		// what the read sees.
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
		time.Sleep(time.Millisecond)
		cache.Invalidate()

		if !cache.ProcessAlive("agent-2", []string{"claude"}) {
			t.Fatal("ProcessAlive(agent-2) = false from a scan that began before its pane existed, want the optimistic true")
		}
		if !cache.ProcessAlive("agent-1", []string{"claude"}) {
			t.Fatal("ProcessAlive(agent-1) = false, want true: its pane is older than the scan")
		}
		if cache.ProcessAlive("agent-1", []string{"codex"}) {
			t.Fatal("ProcessAlive(agent-1, codex) = true, want false: the old scan still speaks for agent-1's older pane")
		}

		// The read above started a fresh scan. It began after agent-2's pane was
		// seen, so once it lands its "no claude under PID 201" is believed.
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		if cache.ProcessAlive("agent-2", []string{"claude"}) {
			t.Fatal("ProcessAlive(agent-2) = true from a scan newer than its pane, want false")
		}
	})
}

// A failed scan keeps the last good snapshot, so a real "dead" still reads as
// dead. Past the staleTTL cliff the snapshot is dropped and ProcessAlive
// degrades optimistically, the same as a cache that never had one. tmux
// liveness never depends on ps.
func TestStateCache_FailedPSKeepsLastGoodUntilStaleTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &mockFetcher{state: shellPaneWithClaude()}
		cache := NewStateCache(f, time.Hour)
		if cache.ProcessAlive("agent-1", []string{"codex"}) {
			t.Fatal("ProcessAlive(agent-1, codex) = true, want false from a good scan")
		}

		failing := shellPaneWithClaude()
		failing.ProcessesAvailable = false
		f.setState(failing)
		cache.mu.Lock()
		cache.procsTTL = time.Millisecond
		cache.mu.Unlock()
		time.Sleep(2 * time.Millisecond)

		if cache.ProcessAlive("agent-1", []string{"codex"}) {
			t.Fatal("ProcessAlive(agent-1, codex) = true while ps fails, want false from the last good snapshot")
		}
		synctest.Wait()
		if got := f.getProcCalls(); got != 2 {
			t.Fatalf("ps scans = %d, want the read to have started a (failing) rescan", got)
		}

		// Only the process snapshot passes the cliff: the list-panes cache has a
		// one-hour TTL and stays fresh.
		time.Sleep(cache.staleTTL + time.Second)
		if !cache.ProcessAlive("agent-1", []string{"codex"}) {
			t.Fatal("ProcessAlive(agent-1, codex) = false past staleTTL, want the optimistic true")
		}
		if !cache.IsRunning("agent-1") {
			t.Fatal("IsRunning(agent-1) = false, want true: tmux liveness does not depend on ps")
		}
		synctest.Wait()
	})
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

	snap, err := f.FetchSessions(context.Background())
	if err == nil {
		t.Fatalf("FetchSessions() err = nil (sessions %+v), want an error for an unreachable server", snap)
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
		// FetchSessions issues exactly one executor call (list-panes); the
		// process-table half reads /proc directly, not through exec. First
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

func TestStateCache_CoalescesRefreshesWithinGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fetcher := &controlledRefreshFetcher{
			state:     runtimeStateSnapshot{Sessions: map[string]sessionRuntimeState{}},
			blockCall: 2,
			entered:   make(chan struct{}),
			release:   make(chan struct{}),
		}
		cache := NewStateCache(fetcher, time.Hour)
		if cache.IsRunning("agent-1") {
			t.Fatal("primed liveness = true, want false")
		}

		fetcher.setResult(runtimeStateSnapshot{
			Sessions: map[string]sessionRuntimeState{"agent-1": {Running: true}},
		}, nil)
		cache.Invalidate()

		results := make(chan bool, 2)
		go func() { results <- cache.IsRunning("agent-1") }()
		synctest.Wait()
		select {
		case <-fetcher.entered:
		default:
			t.Fatal("first invalidated refresh did not enter")
		}

		go func() { results <- cache.IsRunning("agent-1") }()
		synctest.Wait()
		callsBeforeRelease := fetcher.getCalls()
		close(fetcher.release)
		synctest.Wait()

		if callsBeforeRelease != 2 {
			t.Fatalf("fetch calls before release = %d, want prime plus one coalesced generation refresh", callsBeforeRelease)
		}
		for range 2 {
			if got := <-results; !got {
				t.Fatal("coalesced IsRunning call = false, want live result")
			}
		}
	})
}

// TestStateCache_EvictSessionDoesNotMutatePublishedSnapshot pins the
// copy-on-write contract: a snapshot handed out by currentState is read
// without the cache lock, so EvictSession must publish a new Sessions map
// rather than deleting from the one readers may still hold.
func TestStateCache_EvictSessionDoesNotMutatePublishedSnapshot(t *testing.T) {
	f := &mockFetcher{sessions: map[string]bool{"agent-1": true, "agent-2": true}}
	cache := NewStateCache(f, time.Hour)

	published := cache.currentState()
	if !published.Sessions["agent-1"].Running {
		t.Fatal("published snapshot missing agent-1 before eviction")
	}

	f.setResult(map[string]bool{"agent-2": true}, nil)
	cache.EvictSession("agent-1")

	if !published.Sessions["agent-1"].Running || len(published.Sessions) != 2 {
		t.Fatalf("published snapshot mutated by EvictSession: %v", published.Sessions)
	}
	if cache.IsRunning("agent-1") {
		t.Fatal("IsRunning(agent-1) = true after eviction, want false")
	}
	if !cache.IsRunning("agent-2") {
		t.Fatal("IsRunning(agent-2) = false after evicting agent-1, want true")
	}
}

// TestStateCache_EvictSessionDoesNotRaceSnapshotReader reproduces the
// controller crash deterministically under -race: a reader holds a published
// snapshot (as IsRunning/ProcessAlive do after dropping the lock) and reads
// its Sessions map while Stop evicts a session. Deleting from that shared map
// in place is a concurrent map read/write, which is a fatal runtime error that
// recover cannot catch. The handoff below orders only "snapshot taken" before
// the eviction, never the map reads, so the detector sees the conflict
// regardless of scheduling.
func TestStateCache_EvictSessionDoesNotRaceSnapshotReader(t *testing.T) {
	f := &mockFetcher{sessions: map[string]bool{"agent-1": true, "agent-2": true}}
	cache := NewStateCache(f, time.Hour)

	taken := make(chan struct{})
	done := make(chan bool)
	go func() {
		snapshot := cache.currentState()
		close(taken)
		running := false
		for range 1000 {
			running = snapshot.Sessions["agent-1"].Running
		}
		done <- running
	}()

	<-taken
	cache.EvictSession("agent-1")
	if !<-done {
		t.Fatal("reader's snapshot lost agent-1 to a concurrent eviction")
	}
}

// TestStateCache_ConcurrentReadersAndEvictSession drives the same hazard
// through the public API the controller uses: status reads (IsRunning,
// ProcessAlive) racing Stop's EvictSession and Invalidate. Run with -race.
func TestStateCache_ConcurrentReadersAndEvictSession(t *testing.T) {
	names := []string{"agent-1", "agent-2", "agent-3", "agent-4"}
	live := make(map[string]bool, len(names))
	for _, name := range names {
		live[name] = true
	}
	f := &mockFetcher{sessions: live}
	cache := NewStateCache(f, time.Hour)
	if !cache.IsRunning("agent-1") {
		t.Fatal("IsRunning(agent-1) = false after prime, want true")
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				name := names[i%len(names)]
				_ = cache.IsRunning(name)
				_ = cache.ProcessAlive(name, []string{"claude"})
			}
		}()
	}

	for i := range 200 {
		cache.EvictSession(names[i%len(names)])
		if i%10 == 0 {
			cache.Invalidate()
		}
		runtime.Gosched()
	}
	close(stop)
	readers.Wait()
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

// The fleet snapshot must carry attachment and window activity alongside
// liveness so the reconciler can read them without one `display-message` plus
// one `list-windows` fork per session (gcy-8gwi: 14.2% of supervisor CPU).
// Activity is the MAX over every one of the session's pane rows — that is the
// same value the per-session `list-windows -t <s> -F '#{window_activity}'`
// read returns, because every window has at least one pane and all panes of a
// window report their window's activity. It must NOT be
// tmux's #{session_activity}, which does not advance on detached pane I/O and
// differs from max(window_activity) by hours on real sessions.
func TestFetchSessionsCarriesAttachmentAndMaxWindowActivity(t *testing.T) {
	fe := &fakeExecutor{
		// Two sessions. "multi" has three windows with differing activity and
		// a fourth remain-on-exit corpse window holding the NEWEST timestamp;
		// it has TWO attached clients — #{session_attached} is a client count,
		// not a boolean, and must read as attached. "solo" is attached.
		out: strings.Join([]string{
			"multi\t0\tclaude\t101\t2\t1000",
			"multi\t0\tclaude\t102\t2\t3000",
			"multi\t0\tclaude\t103\t2\t2000",
			"multi\t1\tbash\t104\t2\t4000",
			"solo\t0\tcodex\t201\t1\t500",
		}, "\n"),
	}
	f := &tmuxFetcher{tm: &Tmux{cfg: DefaultConfig(), exec: fe}}

	state, err := f.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions() error = %v", err)
	}

	multi := state["multi"]
	if !multi.Running {
		t.Error("multi.Running = false, want true (it has live panes)")
	}
	// "multi" reads session_attached="2" (two clients): a count, not a
	// boolean — it must be reported attached, not detached.
	if !multi.Attached {
		t.Error("multi.Attached = false, want true (2 attached clients)")
	}
	// 4000 comes from the dead pane's row: a remain-on-exit corpse window is
	// still a window, and the per-session list-windows read reports it, so
	// dropping it here would silently make a session look older than tmux says.
	if multi.Activity != 4000 {
		t.Errorf("multi.Activity = %d, want 4000 (max over ALL window rows, corpse window included)", multi.Activity)
	}

	solo := state["solo"]
	if !solo.Attached {
		t.Error("solo.Attached = false, want true")
	}
	if solo.Activity != 500 {
		t.Errorf("solo.Activity = %d, want 500", solo.Activity)
	}

	if len(fe.calls) != 1 {
		t.Fatalf("tmux calls = %d (%v), want 1: the whole fleet's attachment and activity come from the single list-panes snapshot", len(fe.calls), fe.calls)
	}
}

// A session whose only pane is a remain-on-exit corpse is not running, but it
// still has a real window-activity timestamp. Recording the activity without
// setting Running keeps IsRunning/ProcessAlive answering exactly as before
// while letting the activity read be served from the snapshot.
func TestFetchSessionsCorpseSessionKeepsActivityWithoutRunning(t *testing.T) {
	fe := &fakeExecutor{out: "corpse\t1\tbash\t900\t0\t7000"}
	f := &tmuxFetcher{tm: &Tmux{cfg: DefaultConfig(), exec: fe}}

	state, err := f.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions() error = %v", err)
	}
	corpse, ok := state["corpse"]
	if !ok {
		t.Fatal("corpse session missing from snapshot, want an entry carrying its activity")
	}
	if corpse.Running {
		t.Error("corpse.Running = true, want false (its only pane is dead)")
	}
	if corpse.Activity != 7000 {
		t.Errorf("corpse.Activity = %d, want 7000", corpse.Activity)
	}
	if cache := NewStateCache(&mockFetcher{state: runtimeStateSnapshot{Sessions: state}}, time.Hour); cache.IsRunning("corpse") {
		t.Error("IsRunning(corpse) = true, want false: recording activity must not resurrect a dead-pane session")
	}
}

// A session the snapshot has never seen must report a MISS, not a zero
// timestamp or "detached" — the caller falls back to a direct per-session read
// rather than acting on a fabricated value.
func TestStateCacheSessionReadsMissForUnknownSession(t *testing.T) {
	cache := NewStateCache(&mockFetcher{state: runtimeStateSnapshot{
		Sessions: map[string]sessionRuntimeState{"known": {Running: true, Attached: true, Activity: 42}},
	}}, time.Hour)

	if _, ok := cache.SessionActivity("ghost"); ok {
		t.Error("SessionActivity(ghost) ok = true, want false for a session absent from the snapshot")
	}
	if _, ok := cache.SessionAttached("ghost"); ok {
		t.Error("SessionAttached(ghost) ok = true, want false for a session absent from the snapshot")
	}
	activity, ok := cache.SessionActivity("known")
	if !ok || !activity.Equal(time.Unix(42, 0)) {
		t.Errorf("SessionActivity(known) = %v, %t, want %v, true", activity, ok, time.Unix(42, 0))
	}
	attached, ok := cache.SessionAttached("known")
	if !ok || !attached {
		t.Errorf("SessionAttached(known) = %t, %t, want true, true", attached, ok)
	}
}
