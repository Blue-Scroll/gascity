package main

import (
	"maps"
	"sync"

	"github.com/gastownhall/gascity/internal/config"
)

// templateResolveMemo remembers, for one desired-state build, the parts of
// resolveTemplate that depend on the scope and not on the agent.
//
// Why it exists: a live trace (vn-6szu09x, 164 agents, 2026-10-19) put
// resolveTemplate at 335-460 ms a call and about 450 calls a tick. A profile
// showed most of that was the same work redone for every agent in a rig:
//
//   - sessionBackendEnvWithError re-parses the scope's TOML configs, reads the
//     managed Dolt runtime state and forks `ps` to check the Dolt pid.
//   - rawBeadsProviderForScope re-reads the scope's provider config.
//   - defaultBranchFor runs git for a work dir with no recorded branch.
//
// Their answers depend on the scope and on files outside the config, so they
// are remembered for ONE build only and never across ticks: a Dolt restart on
// a new port still reaches the next tick's templates. A failed backend-env
// read is not remembered, so the next agent retries it exactly as before.
//
// One memo is shared by every copy of an agentBuildParams (the field is a
// pointer, like resolveCost), and pool realization may resolve from several
// goroutines, so it takes a lock. Two goroutines may both miss and compute the
// same entry; that costs one extra read and returns the same answer. A nil
// memo remembers nothing and computes every call, which is what a hand-built
// params value in a test or the plan-only allocator gets.
type templateResolveMemo struct {
	mu            sync.Mutex
	backendEnv    map[templateResolveScope]map[string]string
	beadsProvider map[templateResolveScope]string
	defaultBranch map[string]string

	// The loaders the memo stands in front of. newTemplateResolveMemo sets
	// the real ones; a test swaps in counting fakes.
	loadBackendEnv    func(cityPath, rigRoot string, rigs []config.Rig) (map[string]string, error)
	loadBeadsProvider func(scopeRoot, cityPath string) string
	probeBranch       func(dir string) string
}

// templateResolveScope keys a memo entry. cityPath is part of the key even
// though one build has one city, so a memo can never hand one city's Dolt
// target to another.
type templateResolveScope struct {
	cityPath string
	rigRoot  string
}

func newTemplateResolveMemo() *templateResolveMemo {
	return &templateResolveMemo{
		backendEnv:    make(map[templateResolveScope]map[string]string),
		beadsProvider: make(map[templateResolveScope]string),
		defaultBranch: make(map[string]string),

		loadBackendEnv:    sessionBackendEnvWithError,
		loadBeadsProvider: rawBeadsProviderForScope,
		probeBranch:       defaultBranchFor,
	}
}

// sessionBackendEnv is sessionBackendEnvWithError, remembered per scope for
// this build. rigs must be the build's rigs: they are not part of the key,
// because one build has one rig list. The caller owns the returned map.
func (m *templateResolveMemo) sessionBackendEnv(cityPath, rigRoot string, rigs []config.Rig) (map[string]string, error) {
	if m == nil {
		return sessionBackendEnvWithError(cityPath, rigRoot, rigs)
	}
	key := templateResolveScope{cityPath: cityPath, rigRoot: rigRoot}
	m.mu.Lock()
	env, ok := m.backendEnv[key]
	m.mu.Unlock()
	if ok {
		return maps.Clone(env), nil
	}
	env, err := m.loadBackendEnv(cityPath, rigRoot, rigs)
	if err != nil {
		return env, err
	}
	m.mu.Lock()
	m.backendEnv[key] = maps.Clone(env)
	m.mu.Unlock()
	return env, nil
}

// rawBeadsProvider is rawBeadsProviderForScope, remembered per scope for this
// build.
func (m *templateResolveMemo) rawBeadsProvider(scopeRoot, cityPath string) string {
	if m == nil {
		return rawBeadsProviderForScope(scopeRoot, cityPath)
	}
	key := templateResolveScope{cityPath: cityPath, rigRoot: scopeRoot}
	m.mu.Lock()
	provider, ok := m.beadsProvider[key]
	m.mu.Unlock()
	if ok {
		return provider
	}
	provider = m.loadBeadsProvider(scopeRoot, cityPath)
	m.mu.Lock()
	m.beadsProvider[key] = provider
	m.mu.Unlock()
	return provider
}

// defaultBranchForRig is the package defaultBranchForRig with its git probe
// remembered per directory for this build. A rig's recorded branch needs no
// probe and is returned as is. An empty probe answer is not remembered.
func (m *templateResolveMemo) defaultBranchForRig(rigName string, rigs []config.Rig, dir string) string {
	if m == nil {
		return defaultBranchForRig(rigName, rigs, dir)
	}
	if branch := recordedRigDefaultBranch(rigName, rigs); branch != "" {
		return branch
	}
	m.mu.Lock()
	branch, ok := m.defaultBranch[dir]
	m.mu.Unlock()
	if ok {
		return branch
	}
	branch = m.probeBranch(dir)
	if branch != "" {
		m.mu.Lock()
		m.defaultBranch[dir] = branch
		m.mu.Unlock()
	}
	return branch
}
