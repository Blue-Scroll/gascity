package config

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
)

// This file expresses the ga-x9kptu / ga-5736js acceptance criteria at the
// shell-generator level: route-scoped, unassigned pool-demand queries (Tier
// 3, and the reconciler's count-form) must exclude beads carrying any
// hold:<value> label, while the assignee-scoped ephemeral
// probe (Tier 1/2) stays hold-transparent.

func TestBdReadyPoolDemandShellExcludesDispatchHoldLabels(t *testing.T) {
	got := bdReadyPoolDemandShell("--limit 0", QueryTopology{})
	for _, label := range beadmeta.DispatchHoldLabels {
		want := `--exclude-label "` + label + `"`
		if !strings.Contains(got, want) {
			t.Errorf("bdReadyPoolDemandShell() = %q, missing %q", got, want)
		}
	}
}

func TestBdReadyPoolDemandMigrationShellExcludesDispatchHoldLabels(t *testing.T) {
	got := bdReadyPoolDemandMigrationShell("--limit=20", QueryTopology{})
	for _, label := range beadmeta.DispatchHoldLabels {
		want := `--exclude-label "` + label + `"`
		if !strings.Contains(got, want) {
			t.Errorf("bdReadyPoolDemandMigrationShell() = %q, missing %q", got, want)
		}
	}
}

func TestLegacyEphemeralPoolDemandShellRouteScopedExcludesDispatchHoldLabels(t *testing.T) {
	got := legacyEphemeralPoolDemandShell(20, QueryTopology{}, true)
	if !strings.Contains(got, ".labels") {
		t.Errorf("legacyEphemeralPoolDemandShell() = %q, missing a .labels reference", got)
	}
	if !strings.Contains(got, `startswith("`+beadmeta.HoldLabelPrefix+`")`) {
		t.Errorf("legacyEphemeralPoolDemandShell() = %q, missing the hold: family match", got)
	}
}

// TestHoldLabelJQFilterDropsTheWholeHoldFamily runs the shared jq hold clause
// on real rows: every hold:<value> is dropped, hold:squad included, and labels
// that only look hold-ish survive. It is the jq twin of
// beadmeta.TestIsDispatchHoldLabelMatchesTheWholeHoldFamily (vn-ar56o6i).
func TestHoldLabelJQFilterDropsTheWholeHoldFamily(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the work-query shell requires it")
	}
	rows := `[
		{"id":"mayor","labels":["hold:mayor"]},
		{"id":"external","labels":["x","hold:external"]},
		{"id":"squad","labels":["hold:squad"]},
		{"id":"upper","labels":["Hold:Squad"]},
		{"id":"plain","labels":["town-tooling","mpr-human-hold","needs-mayor"]},
		{"id":"none","labels":null},
		{"id":"odd","labels":[7]}
	]`
	cmd := exec.Command("jq", "-c", `[.[]`+excludeHoldLabelsJQClause()+` | .id]`)
	cmd.Stdin = strings.NewReader(rows)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("jq %q: %v", excludeHoldLabelsJQClause(), err)
	}
	if got, want := strings.TrimSpace(string(out)), `["plain","none","odd"]`; got != want {
		t.Fatalf("hold clause kept %s, want %s", got, want)
	}
}

func TestEphemeralAssignedReadyProbeScriptDoesNotExcludeDispatchHoldLabels(t *testing.T) {
	got := ephemeralAssignedReadyProbeScript("cand", QueryTopology{})
	if strings.Contains(got, "--exclude-label") || strings.Contains(got, ".labels") {
		t.Errorf("ephemeralAssignedReadyProbeScript() = %q, assignee-scoped tier must stay hold-transparent", got)
	}
}

func TestEffectiveRoutedPoolQueryCarriesHoldLabelExclusionForLegacyAlias(t *testing.T) {
	a := &Agent{Name: ControlDispatcherAgentName, Dir: "rig"}
	got := a.EffectiveRoutedPoolQuery()
	for _, label := range beadmeta.DispatchHoldLabels {
		want := `--exclude-label "` + label + `"`
		if !strings.Contains(got, want) {
			t.Errorf("EffectiveRoutedPoolQuery() (legacy-alias agent) = %q, missing %q", got, want)
		}
	}
}

func TestPoolDemandCountShellInheritsDispatchHoldLabelExclusion(t *testing.T) {
	got := poolDemandCountShell("hello-world/worker", QueryTopology{})
	for _, label := range beadmeta.DispatchHoldLabels {
		want := `--exclude-label "` + label + `"`
		if !strings.Contains(got, want) {
			t.Errorf("poolDemandCountShell() = %q, missing %q (reconciler count-form must inherit the claim-path fix)", got, want)
		}
	}
}
