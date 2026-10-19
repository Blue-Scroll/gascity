package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
)

func TestTemplateResolveMemoBackendEnvLoadsOncePerScope(t *testing.T) {
	m := newTemplateResolveMemo()
	calls := map[string]int{}
	m.loadBackendEnv = func(_, rigRoot string, _ []config.Rig) (map[string]string, error) {
		calls[rigRoot]++
		return map[string]string{"GC_DOLT_PORT": "3307", "RIG": rigRoot}, nil
	}

	for i := 0; i < 5; i++ {
		for _, rig := range []string{"", "/city/rig-a", "/city/rig-b"} {
			env, err := m.sessionBackendEnv("/city", rig, nil)
			if err != nil {
				t.Fatalf("sessionBackendEnv(%q): %v", rig, err)
			}
			if env["RIG"] != rig {
				t.Fatalf("sessionBackendEnv(%q) RIG = %q, want the env of its own scope", rig, env["RIG"])
			}
		}
	}
	for _, rig := range []string{"", "/city/rig-a", "/city/rig-b"} {
		if calls[rig] != 1 {
			t.Errorf("scope %q loaded %d times in one build, want 1", rig, calls[rig])
		}
	}
}

// A caller that writes into the env it got back must not change what the
// next agent of that scope gets.
func TestTemplateResolveMemoBackendEnvHandsOutCopies(t *testing.T) {
	m := newTemplateResolveMemo()
	m.loadBackendEnv = func(string, string, []config.Rig) (map[string]string, error) {
		return map[string]string{"GC_DOLT_PORT": "3307"}, nil
	}
	first, _ := m.sessionBackendEnv("/city", "/city/rig", nil)
	first["GC_DOLT_PORT"] = "9999"
	second, _ := m.sessionBackendEnv("/city", "/city/rig", nil)
	second["EXTRA"] = "x"
	third, _ := m.sessionBackendEnv("/city", "/city/rig", nil)
	if third["GC_DOLT_PORT"] != "3307" || third["EXTRA"] != "" {
		t.Fatalf("third env = %v, want the loaded env untouched by earlier callers", third)
	}
}

// A failed read is not remembered: the next agent retries it, as it did
// before the memo, instead of failing on a stale error for the whole build.
func TestTemplateResolveMemoBackendEnvRetriesAfterError(t *testing.T) {
	m := newTemplateResolveMemo()
	calls := 0
	m.loadBackendEnv = func(string, string, []config.Rig) (map[string]string, error) {
		calls++
		if calls == 1 {
			return map[string]string{"BEADS_DOLT_AUTO_START": "0"}, errors.New("dolt runtime state unreadable")
		}
		return map[string]string{"GC_DOLT_PORT": "3307"}, nil
	}
	if _, err := m.sessionBackendEnv("/city", "/city/rig", nil); err == nil {
		t.Fatal("first call: want the load error passed through")
	}
	env, err := m.sessionBackendEnv("/city", "/city/rig", nil)
	if err != nil || env["GC_DOLT_PORT"] != "3307" {
		t.Fatalf("second call = %v, %v; want a fresh successful load", env, err)
	}
	if _, err := m.sessionBackendEnv("/city", "/city/rig", nil); err != nil {
		t.Fatalf("third call: %v", err)
	}
	if calls != 2 {
		t.Fatalf("loads = %d, want 2 (the failure, then one remembered success)", calls)
	}
}

func TestTemplateResolveMemoKeysOnCityToo(t *testing.T) {
	m := newTemplateResolveMemo()
	m.loadBeadsProvider = func(scopeRoot, cityPath string) string { return cityPath + "|" + scopeRoot }
	if got := m.rawBeadsProvider("/rig", "/city-a"); got != "/city-a|/rig" {
		t.Fatalf("city-a = %q", got)
	}
	if got := m.rawBeadsProvider("/rig", "/city-b"); got != "/city-b|/rig" {
		t.Fatalf("city-b = %q, want its own answer, not city-a's", got)
	}
}

func TestTemplateResolveMemoDefaultBranch(t *testing.T) {
	m := newTemplateResolveMemo()
	probes := 0
	m.probeBranch = func(dir string) string {
		probes++
		if dir == "/no-git" {
			return ""
		}
		return "trunk"
	}
	rigs := []config.Rig{{Name: "recorded", DefaultBranch: "main"}}

	if got := m.defaultBranchForRig("recorded", rigs, "/w"); got != "main" {
		t.Fatalf("recorded rig = %q, want main", got)
	}
	if probes != 0 {
		t.Fatalf("a rig with a recorded branch probed git %d times, want 0", probes)
	}
	for i := 0; i < 3; i++ {
		if got := m.defaultBranchForRig("", rigs, "/city"); got != "trunk" {
			t.Fatalf("probe = %q, want trunk", got)
		}
	}
	if probes != 1 {
		t.Fatalf("one dir probed %d times in one build, want 1", probes)
	}
	m.defaultBranchForRig("", rigs, "/no-git")
	m.defaultBranchForRig("", rigs, "/no-git")
	if probes != 3 {
		t.Fatalf("probes = %d, want an empty answer re-probed (3)", probes)
	}
}

func TestTemplateResolveMemoNilComputesEveryCall(t *testing.T) {
	var m *templateResolveMemo
	rigs := []config.Rig{{Name: "recorded", DefaultBranch: "release"}}
	if got := m.defaultBranchForRig("recorded", rigs, ""); got != "release" {
		t.Fatalf("nil memo branch = %q, want release", got)
	}
	if got := m.defaultBranchForRig("", nil, ""); got != "main" {
		t.Fatalf("nil memo empty dir = %q, want main", got)
	}
}

func TestTemplateResolveMemoConcurrentUse(t *testing.T) {
	m := newTemplateResolveMemo()
	var mu sync.Mutex
	loads := 0
	m.loadBackendEnv = func(string, string, []config.Rig) (map[string]string, error) {
		mu.Lock()
		loads++
		mu.Unlock()
		return map[string]string{"GC_DOLT_PORT": "3307"}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env, err := m.sessionBackendEnv("/city", "/city/rig", nil)
			if err != nil || env["GC_DOLT_PORT"] != "3307" {
				t.Errorf("concurrent call = %v, %v", env, err)
			}
			env["GC_DOLT_PORT"] = "mutated"
		}()
	}
	wg.Wait()
	if loads < 1 || loads > 32 {
		t.Fatalf("loads = %d", loads)
	}
}

// Every production build gets a memo, so the speedup cannot be lost by a new
// constructor that forgets it.
func TestBaseAgentBuildParamsHasResolveMemo(t *testing.T) {
	p := baseAgentBuildParams("city", t.TempDir(), &config.City{}, nil, time.Time{}, nil, nil)
	if p.resolveMemo == nil {
		t.Fatal("baseAgentBuildParams: resolveMemo is nil; every build must share one memo")
	}
}
