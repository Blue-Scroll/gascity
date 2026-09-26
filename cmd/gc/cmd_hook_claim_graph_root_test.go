package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// Regression coverage for vn-rfn0d9g: a graph.v2 workflow root was handed out
// as pool work.
//
// A graph.v2 run puts two routed, ready rows on the pool at launch: the root and
// its first step. The controller counted both and started two sessions. One
// claimed the step and was handed every other step in the continuation group.
// The other claimed the root, a latch bead only the finalize step closes, and
// had nothing to do. Each controller restart then reopened the parked root and
// did it again: one plan run cost five extra sessions.
//
// The root is not work, so the hook never serves it and the controller never
// counts it (isGraphWorkflowRootContract). The demand half is pinned by the
// agreement corpus in demand_serve_agreement_test.go.

const graphRootTestIdentity = "rig/polecat"

// graphRootWorkQueryRow renders a routed, unassigned graph.v2 workflow root, the
// shape the compiler writes (internal/formula/compile.go).
func graphRootWorkQueryRow(id, status, contract string) string {
	return `{"id":"` + id + `","status":"` + status + `","issue_type":"task","assignee":"","metadata":{"` +
		beadmeta.KindMetadataKey + `":"` + beadmeta.KindWorkflow + `","` +
		beadmeta.FormulaContractMetadataKey + `":"` + contract + `","` +
		beadmeta.RoutedToMetadataKey + `":"` + graphRootTestIdentity + `"}}`
}

func graphRootTestClaimOptions() hookClaimOptions {
	return hookClaimOptions{
		Assignee:           graphRootTestIdentity,
		IdentityCandidates: hookClaimIdentityCandidates(graphRootTestIdentity),
		RouteTargets:       hookClaimRouteTargets(graphRootTestIdentity),
		JSON:               true,
	}
}

// TestHookClaimSkipsGraphWorkflowRootAndClaimsItsStep is the observed sequence:
// the root sorts first (it is the oldest row), and the session must still take
// the step behind it.
func TestHookClaimSkipsGraphWorkflowRootAndClaimsItsStep(t *testing.T) {
	const (
		rootID = "vn-root"
		stepID = "vn-init-run"
	)
	runner := func(string, string) (string, error) {
		return `[
			` + graphRootWorkQueryRow(rootID, "open", beadmeta.FormulaContractGraphV2) + `,
			{"id":"` + stepID + `","status":"open","issue_type":"task","assignee":"","metadata":{"` +
			beadmeta.RoutedToMetadataKey + `":"` + graphRootTestIdentity + `"}}
		]`, nil
	}
	claimedID := ""
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, bool, error) {
			if id == rootID {
				t.Fatalf("store.Claim called for graph.v2 root %q; want its step %q", id, stepID)
			}
			claimedID = id
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Type: "task"}, true, nil
		},
	}
	var stdout, stderr bytes.Buffer
	doHookClaim("bd ready --json", "/tmp/work", graphRootTestClaimOptions(), ops, &stdout, &stderr)

	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.Action != "work" || result.BeadID != stepID {
		t.Fatalf("want step %q served as work, got action=%q reason=%q bead=%q",
			stepID, result.Action, result.Reason, result.BeadID)
	}
	if claimedID != stepID {
		t.Fatalf("store.Claim claimed %q, want %q", claimedID, stepID)
	}
}

// TestHookClaimDrainsWhenOnlyAGraphWorkflowRootIsRouted covers both shapes the
// root was served in: open at launch, and in_progress after a session that held
// it was drained (the parked root).
func TestHookClaimDrainsWhenOnlyAGraphWorkflowRootIsRouted(t *testing.T) {
	for _, status := range []string{"open", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			runner := func(string, string) (string, error) {
				return `[` + graphRootWorkQueryRow("vn-root", status, beadmeta.FormulaContractGraphV2) + `]`, nil
			}
			ops := hookClaimOps{
				Runner: runner,
				Claim: func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, bool, error) {
					t.Fatalf("store.Claim called for graph.v2 root %q; a workflow root is never pool work", id)
					return beads.Bead{}, false, nil
				},
			}
			var stdout, stderr bytes.Buffer
			doHookClaim("bd ready --json", "/tmp/work", graphRootTestClaimOptions(), ops, &stdout, &stderr)

			var result hookClaimJSONResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("stdout is not JSON: %v\nraw: %s", err, stdout.String())
			}
			if result.Action != "drain" || result.Reason != hookClaimReasonNoWork {
				t.Fatalf("want action=drain reason=%s when only a graph.v2 root is routed, got action=%q reason=%q bead=%q",
					hookClaimReasonNoWork, result.Action, result.Reason, result.BeadID)
			}
		})
	}
}

// TestFilterUnreadyHookCandidatesDropsGraphWorkflowRoots pins the shared filter
// directly, with its negative controls: a bead marked only gc.kind=workflow can
// be a root-only wisp whose root IS the work, so it must stay.
func TestFilterUnreadyHookCandidatesDropsGraphWorkflowRoots(t *testing.T) {
	in := `[
		` + graphRootWorkQueryRow("root", "open", beadmeta.FormulaContractGraphV2) + `,
		` + graphRootWorkQueryRow("root-case", "open", "Graph.V2") + `,
		{"id":"kind-only","status":"open","metadata":{"` + beadmeta.KindMetadataKey + `":"` + beadmeta.KindWorkflow + `"}},
		{"id":"step","status":"open","metadata":{"` + beadmeta.RoutedToMetadataKey + `":"` + graphRootTestIdentity + `"}},
		{"id":"nometa","status":"open"},
		{"id":"nullmeta","status":"open","metadata":null}
	]`
	got := filterUnreadyHookCandidates(in, time.Now())

	var rows []map[string]any
	if err := json.Unmarshal([]byte(got), &rows); err != nil {
		t.Fatalf("filtered output is not a JSON array: %v (output %q)", err, got)
	}
	want := []string{"kind-only", "step", "nometa", "nullmeta"}
	if len(rows) != len(want) {
		t.Fatalf("filterUnreadyHookCandidates kept %d rows, want %d: %s", len(rows), len(want), got)
	}
	for i, id := range want {
		if rows[i]["id"] != id {
			t.Fatalf("row %d id = %v, want %q (graph.v2 roots must be dropped, everything else kept): %s", i, rows[i]["id"], id, got)
		}
	}
}
