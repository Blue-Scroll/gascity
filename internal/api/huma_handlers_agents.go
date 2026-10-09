package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/gastownhall/gascity/internal/api/apierr"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"golang.org/x/sync/errgroup"
)

// humaHandleAgentList is the Huma-typed handler for GET /v0/agents. It
// answers through boundedListBuild, so a slow bead store never holds the
// roster back: the phone page shows who is running at once, and the bead
// details fill in when the store read lands (vn-fzant5y). The response cache
// in buildAgentList only helps a request that arrives after a build has
// finished; on the town a crowd of requests arrived together, missed it, and
// each built its own list for minutes (hq-subxy4).
func (s *Server) humaHandleAgentList(ctx context.Context, input *AgentListInput) (*ListOutput[agentResponse], error) {
	bp := input.toBlockingParams()
	if bp.isBlocking() {
		waitForChange(ctx, s.state.EventProvider(), bp)
	}
	return boundedListBuild(&s.listBuilds, cacheKeyFor("agents", input),
		func() (*ListOutput[agentResponse], error) { return s.buildAgentList(input), nil },
		func() *ListOutput[agentResponse] { return s.agentListFromRuntime(input) })
}

// buildAgentList does the work of one full GET /agents request. It reads the
// bead store, so call it through boundedListBuild.
func (s *Server) buildAgentList(input *AgentListInput) *ListOutput[agentResponse] {
	index := s.latestIndex()
	cacheKey := ""
	if !input.Peek {
		// Cache key derived from input struct tags — adding a new query
		// param to AgentListInput automatically participates in the key.
		cacheKey = cacheKeyFor("agents", input)
		if body, ok := cachedResponseAs[ListBody[agentResponse]](s, cacheKey, index); ok {
			return &ListOutput[agentResponse]{
				Index: index,
				Body:  body,
			}
		}
	}

	// Computed after the cache-hit return so a cached response never pays
	// for the store reads.
	body := s.agentListBody(input, &agentStoreReads{
		graphWork:   s.graphActiveWorkBySession(),
		activeBeads: s.newActiveBeadIndex(),
	})
	// A partial list reflects a transient backend failure; don't serve it
	// from cache once the backend recovers.
	if cacheKey != "" && !body.Partial {
		s.storeResponse(cacheKey, index, body)
	}
	return &ListOutput[agentResponse]{
		Index: index,
		Body:  body,
	}
}

// agentListFromRuntime is GET /agents built from config and the runtime
// alone. It never reads the bead store, so it is what boundedListBuild
// answers while the store is slow. It is never put in the response cache: it
// is missing details a full list has.
func (s *Server) agentListFromRuntime(input *AgentListInput) *ListOutput[agentResponse] {
	return &ListOutput[agentResponse]{
		Index: s.latestIndex(),
		Body:  s.agentListBody(input, nil),
	}
}

// agentStoreReads is what the agent rows read from the bead store: active
// graph-resident work by session name (nil on a single-store city), and each
// rig's in-progress beads. A nil *agentStoreReads builds the rows without the
// store; see agentListFromRuntime.
type agentStoreReads struct {
	graphWork   map[string]graphAgentWork
	activeBeads *activeBeadIndex
}

// agentListBody builds the GET /agents rows. reads is nil for the store-free
// list.
func (s *Server) agentListBody(input *AgentListInput, reads *agentStoreReads) ListBody[agentResponse] {
	cfg := s.state.Config()
	sp := s.state.SessionProvider()
	cityName := s.state.CityName()
	sessTmpl := cfg.Workspace.SessionTemplate
	wantPeek := input.Peek

	// Raw config drives accurate provenance detection (pack-derived vs.
	// city-native). Optional capability: when absent, agentOrigin falls
	// back to the patch-presence heuristic.
	var rawCfg *config.City
	if rcp, ok := s.state.(RawConfigProvider); ok {
		rawCfg = rcp.RawConfig()
	}

	var graphWork map[string]graphAgentWork
	if reads != nil {
		graphWork = reads.graphWork
	}
	runningSessions := &runningSessionsOnce{sp: sp}

	// Optional batch extensions: providers whose per-session attribute reads
	// are otherwise expensive (e.g. one subprocess fork per call, per agent)
	// can implement these to collapse the per-row reads to O(1) execs
	// instead of O(agents). SessionRoster is read once, up front, since it
	// covers every session in a single call; EnvironmentBatchProvider is
	// read per running agent (still one call instead of the two GetMeta
	// calls it replaces).
	var rosterMap map[string]runtime.SessionRosterEntry
	if rp, ok := sp.(runtime.SessionRosterProvider); ok {
		if m, err := rp.SessionRoster(); err == nil {
			rosterMap = m
		}
	}
	envBatch, _ := sp.(runtime.EnvironmentBatchProvider)

	// Pool workers run under bead-scoped names that cannot be derived from
	// the member name; see agent_live_session.go. Find them once, from the
	// same listing the pool expansion reads.
	poolSessions := buildPoolSessionIndex(cfg.Agents, runningSessions, sp, envBatch)

	// Pass 1, in order and cheap: expand pools and apply the filters.
	// IsRunning reads the provider's cached session list, and every pool
	// shares runningSessions, so this pass makes no runtime call of its own.
	var rows []agentListRow
	var partialErrors []string
	for _, a := range cfg.Agents {
		// Provenance is a property of the declared agent, shared by every
		// pool-expanded instance, so compute it once per source agent.
		pack, packDerived := agentPackProvenance(a, rawCfg, cfg)
		expanded, expandErr := expandAgentChecked(a, cityName, sessTmpl, runningSessions)
		// A runtime server that is not running at all (tmux before its first
		// session) has no sessions; that is a complete answer, not a failure.
		if runtime.IsRuntimeServerAbsent(expandErr) {
			expandErr = nil
		}
		if isMultiSessionAgent(a) {
			expanded = appendUnlistedPoolMembers(expanded, a, poolSessions)
		}
		switch {
		case expandErr != nil:
			// Say the pool's sessions are unknown rather than listing it as
			// stopped or dropping it silently.
			partialErrors = append(partialErrors, fmt.Sprintf("agent %s: listing pool sessions: %v", a.QualifiedName(), expandErr))
		case len(expanded) == 0 && isMultiSessionAgent(a):
			// An on-demand pool with no live session is still a configured
			// agent: list it under its own name, like an idle singleton.
			expanded = []expandedAgent{configuredPoolRow(a)}
		}
		limits := agentPoolLimits(a)
		for _, ea := range expanded {
			if input.Rig != "" && ea.rig != input.Rig {
				continue
			}
			if input.Pool != "" && ea.pool != input.Pool {
				continue
			}

			sessionName := agentSessionName(cityName, ea.qualifiedName, sessTmpl)
			// Liveness stays on IsRunning: it is already a cached,
			// fleet-wide read that excludes pane_dead corpses, whereas
			// roster membership comes from list-sessions, which still
			// lists a session whose pane exited under remain-on-exit.
			// The roster is an attributes source only.
			running := sp.IsRunning(sessionName)
			var env map[string]string
			if !running && ea.pool != "" {
				if ps, ok := poolSessions.lookup(ea.pool, ea.qualifiedName); ok {
					sessionName, running, env = ps.name, sp.IsRunning(ps.name), ps.env
				}
			}
			var roster *runtime.SessionRosterEntry
			if entry, ok := rosterMap[sessionName]; ok {
				roster = &entry
			}
			// Fold active graph-resident (wisp) work into the running signal so
			// an agent whose work executes under an agent-agnostic wisp session
			// is reported running even when its named provider session is down.
			// hasGraphWork is always false on a single-store city.
			gw, hasGraphWork := graphWork[sessionName]
			effectiveRunning := running || hasGraphWork

			if input.Running == "true" && !effectiveRunning {
				continue
			}
			if input.Running == "false" && effectiveRunning {
				continue
			}
			rows = append(rows, agentListRow{
				agent: a, ea: ea, sessionName: sessionName,
				running: running, env: env, roster: roster, limits: limits,
				graphWork: gw, hasGraphWork: hasGraphWork,
				pack: pack, packDerived: packDerived,
			})
		}
	}

	// Pass 2, in parallel: a running row makes a few runtime calls (tmux
	// execs on this town, about 20ms each under load). Each goroutine fills
	// only its own slot, so the output order stays the config order.
	agents := make([]agentResponse, len(rows))
	group := new(errgroup.Group)
	group.SetLimit(agentListRowConcurrency)
	for i := range rows {
		group.Go(func() error {
			agents[i] = s.agentListResponse(rows[i], cfg, sp, envBatch, reads, wantPeek)
			return nil
		})
	}
	_ = group.Wait()

	if agents == nil {
		agents = []agentResponse{}
	}
	return ListBody[agentResponse]{
		Items:         agents,
		Total:         len(agents),
		Partial:       len(partialErrors) > 0,
		PartialErrors: partialErrors,
	}
}

// agentListRowConcurrency bounds how many agent rows build at once. A running
// row makes a few runtime calls (tmux execs on this town), so a small pool
// cuts the wall time without flooding the provider.
const agentListRowConcurrency = 16

// agentListRow is one agent that passed the list filters, with what pass 1
// already learned about it.
type agentListRow struct {
	agent       config.Agent
	ea          expandedAgent
	sessionName string
	running     bool
	// env is the session's environment when pass 1 already read it to
	// resolve a pool worker, nil otherwise.
	env map[string]string
	// roster is the session's entry in the provider's batched roster, nil
	// when the provider has none or the session is not in it.
	roster       *runtime.SessionRosterEntry
	limits       *poolLimits
	graphWork    graphAgentWork
	hasGraphWork bool
	pack         string
	packDerived  bool
}

// agentListResponse builds one row of GET /v0/agents. It runs on many
// goroutines at once, so it must only read shared state. With a nil reads it
// makes no bead store call at all. envBatch is the provider's batched
// environment read, nil when it has none.
func (s *Server) agentListResponse(row agentListRow, cfg *config.City, sp runtime.Provider, envBatch runtime.EnvironmentBatchProvider, reads *agentStoreReads, wantPeek bool) agentResponse {
	ea, sessionName, running := row.ea, row.sessionName, row.running
	effectiveRunning := running || row.hasGraphWork

	// Live per-session env (suspended flag + GC_SESSION_ID) only exists for a
	// running session. Runtime meta lives on a live session, so only a
	// running one has any to read: asking a stopped tmux session still costs
	// an exec that can only fail, and for every one of the town's 234 to 419
	// slots that was 3 to 6 seconds per list (hq-xujh6g).
	env := row.env
	if running && env == nil && envBatch != nil {
		env, _ = envBatch.GetAllEnvironment(sessionName)
	}
	suspended := ea.suspended
	if running {
		if env != nil {
			if env["suspended"] == "true" {
				suspended = true
			}
		} else if v, err := sp.GetMeta(sessionName, "suspended"); err == nil && v == "true" {
			suspended = true
		}
	}

	provider, displayName := resolveProviderInfo(ea.provider, cfg)

	available := true
	var unavailableReason string
	if suspended {
		available = false
		unavailableReason = "agent is suspended"
	} else if provider != "" {
		if !s.cachedLookPath(providerPathCheck(provider, cfg)) {
			available = false
			unavailableReason = "provider '" + provider + "' not found in PATH"
		}
	}

	resp := agentResponse{
		Name:              ea.qualifiedName,
		Description:       ea.description,
		Running:           effectiveRunning,
		Suspended:         suspended,
		Rig:               ea.rig,
		Pool:              ea.pool,
		Provider:          provider,
		DisplayName:       displayName,
		Available:         available,
		UnavailableReason: unavailableReason,
		PackDerived:       row.packDerived,
		Pack:              row.pack,
		PoolLimits:        row.limits,
	}

	var lastActivity *time.Time
	sessionID := ""
	if running {
		resp.Session = runningSessionInfoFrom(sp, sessionName, row.roster, env)
		lastActivity = resp.Session.LastActivity
		sessionID = resp.Session.ID
	}

	if reads != nil {
		resp.ActiveBead = reads.activeBeads.lookup(ea.rig, sessionID, sessionName, ea.qualifiedName)
	}
	// A relocated-graph wisp names its assignee after the session, not
	// the work store, so the work-store lookup above misses it. Fall
	// back to the graph bead and use its timestamp as the activity
	// signal so the state reads "working", not "stopped".
	if row.hasGraphWork {
		if resp.ActiveBead == "" {
			resp.ActiveBead = row.graphWork.beadID
		}
		if lastActivity == nil {
			la := row.graphWork.lastActivity
			lastActivity = &la
		}
	}
	quarantined := s.state.IsQuarantined(sessionName)
	resp.State = computeAgentState(suspended, quarantined, effectiveRunning, resp.ActiveBead, lastActivity)
	if reads == nil && resp.State == "idle" {
		// computeAgentState reads "no active bead" as idle. Without a store
		// read the bead is unknown, not absent, so a live agent says
		// "running": true, and it makes no claim about the agent's work.
		resp.State = "running"
	}

	if wantPeek && running {
		if output, err := sp.Peek(sessionName, 5); err == nil {
			resp.LastOutput = output
		}
	}

	// enrichSessionMeta resolves the session id through the bead store, so
	// the store-free list leaves model and context for the full list.
	if reads != nil && running && provider == "claude" && canAttributeSession(row.agent, ea.qualifiedName, cfg, s.state.CityPath()) {
		s.enrichSessionMeta(&resp, row.agent, ea.qualifiedName)
	}
	return resp
}

// runningSessionInfoFrom is runningSessionInfo fed from the batch sources the
// list read once per request: the roster entry (attached, last activity) and
// the session's environment (GC_SESSION_ID). A nil roster or env falls back to
// that part's per-session read, so a provider without the batch interfaces
// answers exactly as runningSessionInfo does.
func runningSessionInfoFrom(sp runtime.Provider, sessionName string, roster *runtime.SessionRosterEntry, env map[string]string) *sessionInfo {
	si := &sessionInfo{Name: sessionName}
	if roster != nil {
		if !roster.LastActivity.IsZero() {
			t := roster.LastActivity
			si.LastActivity = &t
		}
		si.Attached = roster.Attached
	} else {
		if t, err := sp.GetLastActivity(sessionName); err == nil && !t.IsZero() {
			si.LastActivity = &t
		}
		si.Attached = sp.IsAttached(sessionName)
	}
	if env != nil {
		si.ID = strings.TrimSpace(env["GC_SESSION_ID"])
	} else if id, err := sp.GetMeta(sessionName, "GC_SESSION_ID"); err == nil {
		si.ID = strings.TrimSpace(id)
	}
	return si
}

// humaHandleAgent is the Huma-typed handler for
// GET /v0/city/{cityName}/agent/{base} (unqualified form).
func (s *Server) humaHandleAgent(_ context.Context, input *AgentGetInput) (*IndexOutput[agentResponse], error) {
	return s.agentByName(input.Name)
}

// humaHandleAgentQualified is the Huma-typed handler for
// GET /v0/city/{cityName}/agent/{dir}/{base} (qualified form).
func (s *Server) humaHandleAgentQualified(_ context.Context, input *AgentGetQualifiedInput) (*IndexOutput[agentResponse], error) {
	return s.agentByName(input.QualifiedName())
}

// agentByName is the shared agent-get implementation. Both the qualified
// and unqualified routes normalize to a single "name" string before
// dispatching here.
func (s *Server) agentByName(name string) (*IndexOutput[agentResponse], error) {
	if name == "" {
		return nil, apierr.InvalidRequest.Msg("agent name required")
	}

	cfg := s.state.Config()
	sp := s.state.SessionProvider()

	agentCfg, ok := findAgent(cfg, name)
	if !ok {
		return nil, apierr.AgentNotFound.Msg("agent " + name + " not found")
	}

	sessionName := s.liveAgentSessionName(name, cfg)
	running := sp.IsRunning(sessionName)
	// Fold active graph-resident (wisp) work into the running signal so a
	// graph-working agent reports running even when its named provider session
	// is down. hasGraphWork is always false on a single-store city.
	gw, hasGraphWork := s.graphActiveWorkBySession()[sessionName]
	effectiveRunning := running || hasGraphWork

	suspended := agentCfg.Suspended
	if v, err := sp.GetMeta(sessionName, "suspended"); err == nil && v == "true" {
		suspended = true
	}

	provider, displayName := resolveProviderInfo(agentCfg.Provider, cfg)

	available := true
	var unavailableReason string
	if suspended {
		available = false
		unavailableReason = "agent is suspended"
	} else if provider != "" {
		if !s.cachedLookPath(providerPathCheck(provider, cfg)) {
			available = false
			unavailableReason = "provider '" + provider + "' not found in PATH"
		}
	}

	var rawCfg *config.City
	if rcp, ok := s.state.(RawConfigProvider); ok {
		rawCfg = rcp.RawConfig()
	}
	pack, packDerived := agentPackProvenance(agentCfg, rawCfg, cfg)

	resp := agentResponse{
		Name:              name,
		Description:       agentCfg.Description,
		Running:           effectiveRunning,
		Suspended:         suspended,
		Rig:               agentCfg.Dir,
		Provider:          provider,
		DisplayName:       displayName,
		Available:         available,
		UnavailableReason: unavailableReason,
		PackDerived:       packDerived,
		Pack:              pack,
		PoolLimits:        agentPoolLimits(agentCfg),
	}
	if isMultiSessionAgent(agentCfg) {
		resp.Pool = agentCfg.QualifiedName()
	}

	var lastActivity *time.Time
	sessionID := ""
	if running {
		resp.Session = runningSessionInfo(sp, sessionName)
		lastActivity = resp.Session.LastActivity
		sessionID = resp.Session.ID
	}

	resp.ActiveBead = s.findLiveActiveBeadForAssignees(agentCfg.Dir, sessionID, sessionName, name)
	// A relocated-graph wisp names its assignee after the session, not the work
	// store, so the work-store lookup above misses it. Fall back to the graph
	// bead and use its timestamp as the activity signal so the state reads
	// "working", not "stopped".
	if hasGraphWork {
		if resp.ActiveBead == "" {
			resp.ActiveBead = gw.beadID
		}
		if lastActivity == nil {
			la := gw.lastActivity
			lastActivity = &la
		}
	}
	quarantined := s.state.IsQuarantined(sessionName)
	resp.State = computeAgentState(suspended, quarantined, effectiveRunning, resp.ActiveBead, lastActivity)

	if running && provider == "claude" && canAttributeSession(agentCfg, name, cfg, s.state.CityPath()) {
		s.enrichSessionMeta(&resp, agentCfg, name)
	}

	return &IndexOutput[agentResponse]{
		Index: s.latestIndex(),
		Body:  resp,
	}, nil
}

// humaHandleAgentCreate is the Huma-typed handler for POST /v0/agents.
// Body validation (Name and Provider required with minLength:"1") is
// enforced by the framework from AgentCreateInput's struct tags.
func (s *Server) humaHandleAgentCreate(ctx context.Context, input *AgentCreateInput) (*AgentCreatedOutput, error) {
	// Idempotency: create at most once per Idempotency-Key. The cached value is
	// the qualified agent name (the response body is rebuilt from it), and the
	// visibility wait stays inside the closure so a cached 201 keeps its strict
	// read-after-write meaning. A create that fails after the durable config
	// write (visibility timeout 503/504) releases the reservation, so a
	// same-key retry re-runs the create and surfaces the conflict — identical
	// to an unkeyed retry today.
	qualifiedName, err := withIdempotency(s.idem, "/v0/agents", input.IdempotencyKey, input.Body,
		func() (string, error) {
			sm, ok := s.state.(StateMutator)
			if !ok {
				return "", errMutationsNotSupported
			}

			a := config.Agent{
				Name:     input.Body.Name,
				Dir:      input.Body.Dir,
				Provider: input.Body.Provider,
				Scope:    input.Body.Scope,
			}

			if err := sm.CreateAgent(a); err != nil {
				return "", mutationError(err)
			}
			// Block until the new agent is reachable through findAgent, so the
			// 201 response is a strict read-after-write signal: a follow-up
			// POST /sling against the same target will not race a stale runtime
			// config snapshot. This is intentionally scoped to agents because sling
			// target resolution reads the agent projection immediately after create.
			name := a.QualifiedName()
			if waiter, ok := s.state.(AgentVisibilityWaiter); ok {
				waitCtx, cancel := context.WithTimeout(ctx, s.agentCreateVisibilityWaitTimeout())
				err := waiter.WaitForAgentVisibility(waitCtx, name)
				cancel()
				if err != nil {
					log.Printf("api: agent %s visibility confirmation failed after create: %v", name, err)
					return "", agentVisibilityWaitHTTPError(err)
				}
			}
			return name, nil
		})
	if err != nil {
		return nil, err
	}
	resp := &AgentCreatedOutput{}
	resp.Body.Status = "created"
	resp.Body.Agent = qualifiedName
	return resp, nil
}

func agentVisibilityWaitHTTPError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return agentVisibilityRetryableError(apierr.ServiceUnavailable.Msg("agent was created, but visibility confirmation was canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return agentVisibilityRetryableError(apierr.GatewayTimeout.Msg("agent was created, but visibility was not confirmed before timeout"))
	default:
		return apierr.Internal.Msg("agent was created, but visibility confirmation failed")
	}
}

func agentVisibilityRetryableError(err error) error {
	return huma.ErrorWithHeaders(err, http.Header{"Retry-After": []string{"1"}})
}

// humaHandleAgentUpdate is the Huma-typed handler for
// PATCH /v0/city/{cityName}/agent/{base}.
func (s *Server) humaHandleAgentUpdate(_ context.Context, input *AgentUpdateInput) (*OKResponse, error) {
	return s.updateAgentByName(input.Name, input.Body.Provider, input.Body.Scope, input.Body.Suspended)
}

// humaHandleAgentUpdateQualified is the Huma-typed handler for
// PATCH /v0/city/{cityName}/agent/{dir}/{base}.
func (s *Server) humaHandleAgentUpdateQualified(_ context.Context, input *AgentUpdateQualifiedInput) (*OKResponse, error) {
	return s.updateAgentByName(input.QualifiedName(), input.Body.Provider, input.Body.Scope, input.Body.Suspended)
}

func (s *Server) updateAgentByName(name, provider, scope string, suspended *bool) (*OKResponse, error) {
	sm, ok := s.state.(StateMutator)
	if !ok {
		return nil, errMutationsNotSupported
	}
	patch := AgentUpdate{Provider: provider, Scope: scope, Suspended: suspended}
	if err := sm.UpdateAgent(name, patch); err != nil {
		return nil, mutationError(err)
	}
	resp := &OKResponse{}
	resp.Body.Status = "updated"
	return resp, nil
}

// humaHandleAgentDelete is the Huma-typed handler for
// DELETE /v0/city/{cityName}/agent/{base}.
func (s *Server) humaHandleAgentDelete(_ context.Context, input *AgentDeleteInput) (*OKResponse, error) {
	return s.deleteAgentByName(input.Name)
}

// humaHandleAgentDeleteQualified is the Huma-typed handler for
// DELETE /v0/city/{cityName}/agent/{dir}/{base}.
func (s *Server) humaHandleAgentDeleteQualified(_ context.Context, input *AgentDeleteQualifiedInput) (*OKResponse, error) {
	return s.deleteAgentByName(input.QualifiedName())
}

func (s *Server) deleteAgentByName(name string) (*OKResponse, error) {
	sm, ok := s.state.(StateMutator)
	if !ok {
		return nil, errMutationsNotSupported
	}
	if err := sm.DeleteAgent(name); err != nil {
		return nil, mutationError(err)
	}
	resp := &OKResponse{}
	resp.Body.Status = "deleted"
	return resp, nil
}

// humaHandleAgentAction is the Huma-typed handler for
// POST /v0/city/{cityName}/agent/{base}/{action}.
func (s *Server) humaHandleAgentAction(_ context.Context, input *AgentActionInput) (*OKResponse, error) {
	return s.agentActionByName(input.Name, input.Action)
}

// humaHandleAgentActionQualified is the Huma-typed handler for
// POST /v0/city/{cityName}/agent/{dir}/{base}/{action}.
func (s *Server) humaHandleAgentActionQualified(_ context.Context, input *AgentActionQualifiedInput) (*OKResponse, error) {
	return s.agentActionByName(input.QualifiedName(), input.Action)
}

func (s *Server) agentActionByName(name, action string) (*OKResponse, error) {
	sm, ok := s.state.(StateMutator)
	if !ok {
		return nil, errMutationsNotSupported
	}
	cfg := s.state.Config()
	if _, ok := findAgent(cfg, name); !ok {
		return nil, apierr.AgentNotFound.Msg("agent " + name + " not found")
	}
	var err error
	switch action {
	case "suspend":
		err = sm.SuspendAgent(name)
	case "resume":
		err = sm.ResumeAgent(name)
	default:
		return nil, apierr.InvalidRequest.Msg("unknown agent action: " + action)
	}
	if err != nil {
		return nil, mutationError(err)
	}
	resp := &OKResponse{}
	resp.Body.Status = "ok"
	return resp, nil
}

// humaHandleAgentOutput is the Huma-typed handler for GET /v0/agent/{base}/output
// (unqualified agent name, no rig prefix).
func (s *Server) humaHandleAgentOutput(_ context.Context, input *AgentOutputInput) (*struct {
	Body agentOutputResponse
}, error,
) {
	tail, provided := input.Compactions()
	return s.agentOutputByName(input.Name, tail, provided, input.Before)
}

// humaHandleAgentOutputQualified is the Huma-typed handler for
// GET /v0/agent/{dir}/{base}/output (qualified agent name with rig prefix).
func (s *Server) humaHandleAgentOutputQualified(_ context.Context, input *AgentOutputQualifiedInput) (*struct {
	Body agentOutputResponse
}, error,
) {
	tail, provided := input.Compactions()
	return s.agentOutputByName(input.QualifiedName(), tail, provided, input.Before)
}

// agentOutputByName is the shared implementation for the agent output
// handlers. tail carries the client's ?tail= value verbatim; provided
// reports whether the client supplied ?tail= at all. When provided is
// false, the handler applies the default (1 compaction). When provided
// is true and tail==0, return all compactions (sessionlog's
// "no pagination" mode).
func (s *Server) agentOutputByName(name string, tail int, provided bool, before string) (*struct {
	Body agentOutputResponse
}, error,
) {
	cfg := s.state.Config()
	agentCfg, ok := findAgent(cfg, name)
	if !ok {
		return nil, apierr.AgentNotFound.Msg("agent " + name + " not found")
	}

	resp, err := s.trySessionLogOutputHuma(name, agentCfg, tail, provided, before)
	if err != nil {
		return nil, apierr.Internal.Msg("reading session log: " + err.Error())
	}
	if resp != nil {
		return &struct {
			Body agentOutputResponse
		}{Body: *resp}, nil
	}

	// No session file found — fall back to Peek() (raw terminal text).
	sp := s.state.SessionProvider()
	sessionName := s.liveAgentSessionName(name, cfg)
	if !sp.IsRunning(sessionName) {
		return nil, apierr.AgentNotFound.Msg("agent " + name + " not running")
	}

	output, err := sp.Peek(sessionName, 100)
	if err != nil {
		return nil, apierr.Internal.Msg(err.Error())
	}

	turns := []outputTurn{}
	if output != "" {
		turns = append(turns, outputTurn{Role: "output", Text: output})
	}

	return &struct {
		Body agentOutputResponse
	}{Body: agentOutputResponse{
		Agent:  name,
		Format: "text",
		Turns:  turns,
	}}, nil
}

// agentStreamState holds state resolved during the agent output stream
// precheck that the streaming callback needs. Both phases call
// resolveAgentStream() so precheck failures turn into proper HTTP errors
// before the SSE response is committed.
type agentStreamState struct {
	name           string
	logPath        string
	provider       string
	running        bool
	cfg            *config.City
	resolveLogPath func() string
}

// resolveAgentStream is shared between the precheck and stream callback.
// Returns the resolved state or an HTTP error if the agent doesn't exist
// or has no output available.
func (s *Server) resolveAgentStream(name string) (*agentStreamState, error) {
	cfg := s.state.Config()
	agentCfg, ok := findAgent(cfg, name)
	if !ok {
		return nil, apierr.AgentNotFound.Msg("agent " + name + " not found")
	}

	workDir := s.resolveAgentWorkDir(agentCfg, name)
	transcriptState, err := s.resolveAgentTranscript(name, agentCfg)
	if err != nil {
		return nil, apierr.Internal.Msg(err.Error())
	}
	provider := transcriptState.provider
	logPath := transcriptState.path

	sp := s.state.SessionProvider()
	sessionName := transcriptState.sessionName
	running := sp.IsRunning(sessionName)

	if logPath == "" && !running {
		return nil, apierr.AgentNotFound.Msg("agent " + name + " not running")
	}
	return &agentStreamState{
		name:     name,
		logPath:  logPath,
		provider: provider,
		running:  running,
		cfg:      cfg,
		resolveLogPath: func() string {
			if workDir == "" {
				return ""
			}
			resolved, err := s.resolveAgentTranscript(name, agentCfg)
			if err != nil {
				return ""
			}
			return resolved.path
		},
	}, nil
}

func (s *Server) checkAgentOutputStream(_ context.Context, input *AgentOutputStreamInput) error {
	_, err := s.resolveAgentStream(input.Base)
	return err
}

func (s *Server) streamAgentOutput(hctx huma.Context, input *AgentOutputStreamInput, send sse.Sender) {
	s.doStreamAgentOutput(hctx, input.Base, send)
}

func (s *Server) checkAgentOutputStreamQualified(_ context.Context, input *AgentOutputStreamQualifiedInput) error {
	_, err := s.resolveAgentStream(input.QualifiedName())
	return err
}

func (s *Server) streamAgentOutputQualified(hctx huma.Context, input *AgentOutputStreamQualifiedInput, send sse.Sender) {
	s.doStreamAgentOutput(hctx, input.QualifiedName(), send)
}

// doStreamAgentOutput is the shared streaming implementation.
func (s *Server) doStreamAgentOutput(hctx huma.Context, name string, send sse.Sender) {
	state, err := s.resolveAgentStream(name)
	if err != nil {
		return
	}
	if !state.running {
		hctx.SetHeader("GC-Agent-Status", "stopped")
	}
	flushSSEHeaders(hctx)
	ctx := hctx.Context()
	workerOps := s.watchAgentWorkerOperationSignals(ctx, state.name, state.cfg)
	if state.logPath != "" {
		s.streamSessionLogHuma(ctx, send, state.name, state.provider, state.logPath, state.resolveLogPath, workerOps)
	} else {
		s.streamPeekOutputHuma(ctx, send, state.name, s.agentWorkerHandle(state.name, state.cfg), workerOps)
	}
}
