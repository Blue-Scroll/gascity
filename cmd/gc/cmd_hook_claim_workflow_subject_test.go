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

// Regression coverage for vn-dpowwra: the subject of a graph workflow was handed
// out as pool work.
//
// `gc sling <pool> <leg> --on mol-review-leg` starts a graph workflow for the
// review leg. Its steps carry gc.routed_to and are the work. The leg itself, the
// subject, carries only gc.execution_routed_to. On 2026-09-29 three legs got a
// hand-written gc.routed_to as well, so the pool's tier-3 query served each bare
// leg as free work while its workflow's step went to another polecat. Each leg
// was worked twice.
//
// So the hook never hands out an unassigned subject and the controller never
// counts one (isWorkflowSubject). The demand half is pinned by the agreement
// corpus in demand_serve_agreement_test.go.

const workflowSubjectTestIdentity = "rig/polecat"

// workflowSubjectWorkQueryRow renders a review leg the way it looked on
// 2026-09-29: unassigned, with both routes naming the pool.
func workflowSubjectWorkQueryRow(id, assignee string) string {
	return `{"id":"` + id + `","status":"open","issue_type":"task","assignee":"` + assignee + `","metadata":{"` +
		beadmeta.ExecutionRoutedToMetadataKey + `":"` + workflowSubjectTestIdentity + `","` +
		beadmeta.RoutedToMetadataKey + `":"` + workflowSubjectTestIdentity + `"}}`
}

func workflowSubjectTestClaimOptions() hookClaimOptions {
	return hookClaimOptions{
		Assignee:           workflowSubjectTestIdentity,
		IdentityCandidates: hookClaimIdentityCandidates(workflowSubjectTestIdentity),
		RouteTargets:       hookClaimRouteTargets(workflowSubjectTestIdentity),
		JSON:               true,
	}
}

// TestHookClaimSkipsWorkflowSubjectAndClaimsTheStep: the subject sorts first
// (it is older than its steps), and the session must still take the step.
func TestHookClaimSkipsWorkflowSubjectAndClaimsTheStep(t *testing.T) {
	const (
		subjectID = "vn-leg"
		stepID    = "vn-leg-step"
	)
	runner := func(string, string) (string, error) {
		return `[
			` + workflowSubjectWorkQueryRow(subjectID, "") + `,
			{"id":"` + stepID + `","status":"open","issue_type":"task","assignee":"","metadata":{"` +
			beadmeta.RoutedToMetadataKey + `":"` + workflowSubjectTestIdentity + `"}}
		]`, nil
	}
	claimedID := ""
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, bool, error) {
			if id == subjectID {
				t.Fatalf("store.Claim called for workflow subject %q; want its step %q", id, stepID)
			}
			claimedID = id
			return beads.Bead{ID: id, Status: "in_progress", Assignee: assignee, Type: "task"}, true, nil
		},
	}
	var stdout, stderr bytes.Buffer
	doHookClaim("bd ready --json", "/tmp/work", workflowSubjectTestClaimOptions(), ops, &stdout, &stderr)

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

// TestHookClaimDrainsWhenOnlyAWorkflowSubjectIsRouted is the 2026-09-29 shape
// with the step already taken: the bare leg is all that is left on the pool,
// and nobody may claim it.
func TestHookClaimDrainsWhenOnlyAWorkflowSubjectIsRouted(t *testing.T) {
	runner := func(string, string) (string, error) {
		return `[` + workflowSubjectWorkQueryRow("vn-leg", "") + `]`, nil
	}
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, bool, error) {
			t.Fatalf("store.Claim called for workflow subject %q; its workflow's steps are the work", id)
			return beads.Bead{}, false, nil
		},
	}
	var stdout, stderr bytes.Buffer
	doHookClaim("bd ready --json", "/tmp/work", workflowSubjectTestClaimOptions(), ops, &stdout, &stderr)

	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.Action != "drain" || result.Reason != hookClaimReasonNoWork {
		t.Fatalf("want action=drain reason=%s when only a workflow subject is routed, got action=%q reason=%q bead=%q",
			hookClaimReasonNoWork, result.Action, result.Reason, result.BeadID)
	}
}

// TestFilterUnreadyHookCandidatesDropsUnassignedWorkflowSubjects pins the shared
// filter directly, with its negative controls:
//   - a subject somebody already holds stays, for that holder's crash recovery;
//   - a workflow step and a control step carry gc.execution_routed_to too, and
//     both are real work for whoever their gc.routed_to names.
func TestFilterUnreadyHookCandidatesDropsUnassignedWorkflowSubjects(t *testing.T) {
	in := `[
		` + workflowSubjectWorkQueryRow("subject", "") + `,
		` + workflowSubjectWorkQueryRow("held-subject", "rig/polecat-2") + `,
		{"id":"step","status":"open","metadata":{"` +
		beadmeta.ExecutionRoutedToMetadataKey + `":"` + workflowSubjectTestIdentity + `","` +
		beadmeta.RootBeadIDMetadataKey + `":"vn-root"}},
		{"id":"finalize","status":"open","metadata":{"` +
		beadmeta.KindMetadataKey + `":"` + beadmeta.KindWorkflowFinalize + `","` +
		beadmeta.ExecutionRoutedToMetadataKey + `":"` + workflowSubjectTestIdentity + `","` +
		beadmeta.RoutedToMetadataKey + `":"rig/control-dispatcher"}},
		{"id":"plain","status":"open","metadata":{"` + beadmeta.RoutedToMetadataKey + `":"` + workflowSubjectTestIdentity + `"}},
		{"id":"blank-route","status":"open","metadata":{"` + beadmeta.ExecutionRoutedToMetadataKey + `":"  "}},
		{"id":"nullmeta","status":"open","metadata":null}
	]`
	got := filterUnreadyHookCandidates(in, time.Now())

	var rows []map[string]any
	if err := json.Unmarshal([]byte(got), &rows); err != nil {
		t.Fatalf("filtered output is not a JSON array: %v (output %q)", err, got)
	}
	want := []string{"held-subject", "step", "finalize", "plain", "blank-route", "nullmeta"}
	if len(rows) != len(want) {
		t.Fatalf("filterUnreadyHookCandidates kept %d rows, want %d: %s", len(rows), len(want), got)
	}
	for i, id := range want {
		if rows[i]["id"] != id {
			t.Fatalf("row %d id = %v, want %q (only the unassigned subject may be dropped): %s", i, rows[i]["id"], id, got)
		}
	}
}
