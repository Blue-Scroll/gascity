package main

import (
	"sync"
	"time"
)

// templateResolveCost adds up the time one desired-state build spends turning
// agents into session templates (resolveTemplatePrepared). It splits that time
// into its three steps, because they cost very different things:
//
//   - validate: find the provider binary on PATH and check its transport.
//   - prepare: copy overlay folders into the work dir and install hooks.
//     This writes files, for every desired session, on every tick.
//   - resolve: build the template itself (prompt, env, fingerprint).
//
// Why it exists: on 2026-10-05 the untimed tail of load_demand_snapshot cost
// 38s to 157s per tick, with almost no bd calls in it (vn-z3xmcnp). Template
// resolution runs once per desired session in that tail and is the first
// suspect. The demand sub-phase records carry this split so the next trace
// says whether it is the cause, and which step.
//
// One cost is shared by every copy of an agentBuildParams (the field is a
// pointer), and pool realization may resolve from several goroutines, so it
// takes a lock. A nil cost records nothing.
type templateResolveCost struct {
	mu       sync.Mutex
	calls    int
	validate time.Duration
	prepare  time.Duration
	resolve  time.Duration
}

// templateResolveTotals is a point-in-time copy of a templateResolveCost.
type templateResolveTotals struct {
	calls    int
	validate time.Duration
	prepare  time.Duration
	resolve  time.Duration
}

func (c *templateResolveCost) add(validate, prepare, resolve time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.validate += validate
	c.prepare += prepare
	c.resolve += resolve
}

func (c *templateResolveCost) totals() templateResolveTotals {
	if c == nil {
		return templateResolveTotals{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return templateResolveTotals{calls: c.calls, validate: c.validate, prepare: c.prepare, resolve: c.resolve}
}

// addSince writes the resolution cost since before into fields, as
// templates_resolved plus one <step>_ms field per step. It returns fields so a
// call site can build the map inline.
func (c *templateResolveCost) addSince(before templateResolveTotals, fields map[string]any) map[string]any {
	if fields == nil {
		fields = make(map[string]any, 4)
	}
	now := c.totals()
	fields["templates_resolved"] = now.calls - before.calls
	fields["template_validate_ms"] = (now.validate - before.validate).Milliseconds()
	fields["template_prepare_ms"] = (now.prepare - before.prepare).Milliseconds()
	fields["template_resolve_ms"] = (now.resolve - before.resolve).Milliseconds()
	return fields
}
