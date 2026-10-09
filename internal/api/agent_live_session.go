package api

import (
	"path"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"golang.org/x/sync/errgroup"
)

// A pool worker does not run under its agent's derived session name.
//
// Since #6549 an unaliased pool session runs under a bead-scoped name,
// PoolSessionName in cmd/gc: <basename(pool)>-<session bead id>. On the town,
// vessel-network/dune runs as "polecat-opus-high-hq-wisp-7kuf85v", not as the
// "vessel-network--dune" that agent.SessionNameFor derives. Nothing in the
// name says which member it is. The session's own environment does:
// GC_TEMPLATE names the pool and GC_ALIAS names the member.
//
// So an agent handler that only derives the name reports every pool worker
// stopped. On 2026-10-09 the Agents tab showed 4 of 26 live agents that way
// (vn-kpk61ob). Resolve an agent's live session through liveAgentSessionName
// or poolSessionIndex, never through agentSessionName alone.

// poolSession is one live pool worker, found by its environment.
type poolSession struct {
	// name is the runtime session name.
	name string
	// alias is GC_ALIAS, the qualified member name ("vessel-network/dune").
	alias string
	// template is GC_TEMPLATE, the pool's qualified name.
	template string
	// env is the session's full environment when the provider reads it in
	// one call, so the agent row can reuse it instead of reading it again.
	// Nil when the provider has no batch read.
	env map[string]string
}

// poolSessionIndex is the live pool workers of one request, by pool and
// member. Keyed by both, so a session that names a member of another pool
// can never stand in for that member.
type poolSessionIndex struct {
	byMember map[poolMember]poolSession
}

type poolMember struct{ pool, alias string }

// beadScopedPoolPrefix is the runtime-name prefix of a pool's bead-scoped
// sessions. It must match PoolSessionName in cmd/gc. Pools in two rigs that
// share a base name share this prefix too, so a name match is only a
// candidate until GC_TEMPLATE confirms the pool.
func beadScopedPoolPrefix(poolName string) string {
	return agent.SanitizeQualifiedNameForSession(path.Base(poolName)) + "-"
}

// buildPoolSessionIndex finds the live bead-scoped sessions of the given
// pools. It lists the runtime once and reads the environment only of the
// sessions whose name carries a pool prefix, in parallel. A listing error
// gives an empty index: callers then fall back to the derived name, which is
// what they did before this index existed.
func buildPoolSessionIndex(agents []config.Agent, running sessionLister, sp runtime.Provider, envBatch runtime.EnvironmentBatchProvider) poolSessionIndex {
	ix := poolSessionIndex{byMember: map[poolMember]poolSession{}}
	if running == nil || sp == nil {
		return ix
	}
	pools := map[string]bool{}
	var prefixes []string
	for _, a := range agents {
		if !isMultiSessionAgent(a) {
			continue
		}
		pools[a.QualifiedName()] = true
		prefixes = append(prefixes, beadScopedPoolPrefix(a.QualifiedName()))
	}
	if len(pools) == 0 {
		return ix
	}
	all, err := running.ListRunning("")
	if err != nil {
		return ix
	}
	var candidates []string
	for _, name := range all {
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				candidates = append(candidates, name)
				break
			}
		}
	}
	// Sorted, so two sessions that claim one member always resolve the same
	// way: the first name wins.
	sort.Strings(candidates)

	found := make([]poolSession, len(candidates))
	ok := make([]bool, len(candidates))
	group := new(errgroup.Group)
	group.SetLimit(agentListRowConcurrency)
	for i, name := range candidates {
		group.Go(func() error {
			found[i], ok[i] = readPoolSession(sp, envBatch, name)
			return nil
		})
	}
	_ = group.Wait()

	for i, ps := range found {
		if !ok[i] || !pools[ps.template] {
			continue
		}
		key := poolMember{pool: ps.template, alias: ps.alias}
		if _, taken := ix.byMember[key]; taken {
			continue
		}
		ix.byMember[key] = ps
	}
	return ix
}

// readPoolSession reads which pool member a session is from its environment.
// It reports false when the session names no pool or member.
func readPoolSession(sp runtime.Provider, envBatch runtime.EnvironmentBatchProvider, name string) (poolSession, bool) {
	ps := poolSession{name: name}
	if envBatch != nil {
		env, err := envBatch.GetAllEnvironment(name)
		if err != nil {
			return ps, false
		}
		ps.env = env
		ps.alias = strings.TrimSpace(env["GC_ALIAS"])
		ps.template = strings.TrimSpace(env["GC_TEMPLATE"])
	} else {
		alias, err := sp.GetMeta(name, "GC_ALIAS")
		if err != nil {
			return ps, false
		}
		template, err := sp.GetMeta(name, "GC_TEMPLATE")
		if err != nil {
			return ps, false
		}
		ps.alias = strings.TrimSpace(alias)
		ps.template = strings.TrimSpace(template)
	}
	return ps, ps.alias != "" && ps.template != ""
}

// lookup returns the live session of pool member qualifiedName.
func (ix poolSessionIndex) lookup(pool, qualifiedName string) (poolSession, bool) {
	ps, ok := ix.byMember[poolMember{pool: pool, alias: qualifiedName}]
	return ps, ok
}

// unlisted returns the live members of pool that are not in listed, by name.
// An unlimited pool's discovery matches derived names only, and a bounded
// pool lists only its configured slots, which a live member can outlive when
// the pool's max is lowered. Without these, such a member has no row at all.
func (ix poolSessionIndex) unlisted(pool string, listed map[string]bool) []poolSession {
	var out []poolSession
	for key, ps := range ix.byMember {
		if key.pool == pool && !listed[key.alias] {
			out = append(out, ps)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].alias < out[j].alias })
	return out
}

// appendUnlistedPoolMembers adds a row for each live member of pool a that
// the expansion did not list, after the rows it did.
func appendUnlistedPoolMembers(expanded []expandedAgent, a config.Agent, ix poolSessionIndex) []expandedAgent {
	pool := a.QualifiedName()
	listed := make(map[string]bool, len(expanded))
	for _, ea := range expanded {
		listed[ea.qualifiedName] = true
	}
	for _, ps := range ix.unlisted(pool, listed) {
		// A member of this pool lives in this pool's rig. Any other alias
		// is a stray that would show as a second row for another agent.
		if dir, _ := config.ParseQualifiedName(ps.alias); dir != a.Dir {
			continue
		}
		expanded = append(expanded, expandedAgent{
			qualifiedName: ps.alias,
			rig:           a.Dir,
			pool:          pool,
			suspended:     a.Suspended,
			provider:      a.Provider,
			description:   a.Description,
		})
	}
	return expanded
}

// liveAgentSessionName is the runtime session name the named agent runs
// under. That is the derived name when its session is live there, and for a
// pool member whose derived session is not live, the bead-scoped session
// whose environment names it. Anything unresolved gets the derived name,
// which is what every caller used before.
func (s *Server) liveAgentSessionName(name string, cfg *config.City) string {
	canonical := agentSessionName(s.state.CityName(), name, cfg.Workspace.SessionTemplate)
	sp := s.state.SessionProvider()
	if sp == nil || sp.IsRunning(canonical) {
		return canonical
	}
	agentCfg, ok := findAgent(cfg, name)
	if !ok || !isMultiSessionAgent(agentCfg) {
		return canonical
	}
	envBatch, _ := sp.(runtime.EnvironmentBatchProvider)
	ix := buildPoolSessionIndex([]config.Agent{agentCfg}, sp, sp, envBatch)
	if ps, ok := ix.lookup(agentCfg.QualifiedName(), name); ok {
		return ps.name
	}
	return canonical
}
