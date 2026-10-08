package main

import (
	"testing"
	"time"
)

func TestTemplateResolveCostNilRecordsNothing(t *testing.T) {
	var c *templateResolveCost
	c.add(time.Second, time.Second, time.Second)
	fields := c.addSince(c.totals(), nil)
	if fields["templates_resolved"] != 0 {
		t.Fatalf("templates_resolved = %v, want 0 for a nil cost", fields["templates_resolved"])
	}
}

func TestTemplateResolveCostAddSinceReportsOnlyTheDelta(t *testing.T) {
	c := &templateResolveCost{}
	c.add(1*time.Millisecond, 10*time.Millisecond, 100*time.Millisecond)
	before := c.totals()
	c.add(2*time.Millisecond, 20*time.Millisecond, 200*time.Millisecond)
	c.add(3*time.Millisecond, 30*time.Millisecond, 300*time.Millisecond)

	fields := c.addSince(before, map[string]any{"pools": 4})
	want := map[string]any{
		"pools":                4,
		"templates_resolved":   2,
		"template_validate_ms": int64(5),
		"template_prepare_ms":  int64(50),
		"template_resolve_ms":  int64(500),
	}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("%s = %v (%T), want %v (%T)", k, fields[k], fields[k], v, v)
		}
	}
}

// A copied agentBuildParams (resolveTemplateForSessionBeadInfo copies it) must
// still add to the build's one total, or the overlay's resolves go missing.
func TestTemplateResolveCostIsSharedByCopiedBuildParams(t *testing.T) {
	bp := &agentBuildParams{resolveCost: &templateResolveCost{}}
	local := *bp
	local.resolveCost.add(0, time.Millisecond, 0)
	if got := bp.resolveCost.totals().calls; got != 1 {
		t.Fatalf("calls seen through the original params = %d, want 1", got)
	}
}
