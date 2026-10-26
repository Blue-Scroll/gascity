package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// A squad member is a child of its squad bead, labeled hold:squad, with no
// assignee and no route. A stray gc.routed_to left on one is exactly how a
// pool polecat was handed a member to build beside its squad (vn-c4jeecr:
// vn-7k4mroa, vn-v5zrwhu). These tests run the generated routed tier and the
// reconciler's count-form against a fake bd that returns the held rows anyway,
// the way a bd reads a hold value its --exclude-label flags do not name, so
// they pin the jq layer on its own. The bd-side flag is pinned separately.
//
// The assigned tiers are out of scope on purpose: they stay hold-transparent so
// a held assignment keeps its owner visible to the pool and to crash recovery
// (ga-5736js), and the hook strips a held row before serving it
// (isHeldHookCandidate, pinned by cmd/gc's hook claim hold-label tests).

const holdSquadPoolTarget = "worker-pool"

func holdSquadPoolAgent() Agent {
	return Agent{Name: "worker", PoolName: holdSquadPoolTarget}
}

// fakeBdRoutedRows answers the routed read for holdSquadPoolTarget with rows
// and every other read (the legacy run_target tier, the ephemeral scan) with
// an empty array. It ignores --exclude-label, so a held row it returns can only
// be dropped by the generated jq.
func fakeBdRoutedRows(rows string) string {
	return "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *\"gc.routed_to=" + holdSquadPoolTarget + "\"*) printf '%s' '" + rows + "' ;;\n" +
		"  *) printf '[]' ;;\n" +
		"esac\n"
}

const (
	holdSquadMemberRow = `{"id":"vn-member","status":"open","labels":["hold:squad","squads"],"metadata":{"gc.routed_to":"worker-pool"}}`
	holdOtherRow       = `{"id":"vn-other-hold","status":"open","labels":["Hold:Later"],"metadata":{"gc.routed_to":"worker-pool"}}`
	// The plain label "squad" marks a squad bead, which is not a hold: the
	// captain claims it, so it must still be served.
	plainSquadLabelRow = `{"id":"vn-plain","status":"open","labels":["squad"],"metadata":{"gc.routed_to":"worker-pool"}}`
)

func servedIDs(t *testing.T, out string) []string {
	t.Helper()
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("work query output is not a JSON array: %v (output %q)", err, out)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestRoutedWorkQueryNeverServesAHoldSquadMember(t *testing.T) {
	a := holdSquadPoolAgent()
	rows := "[" + holdSquadMemberRow + "," + holdOtherRow + "," + plainSquadLabelRow + "]"

	out := runShellWithFakeBd(t, a.EffectiveWorkQueryFor(QueryTopology{}), nil, fakeBdRoutedRows(rows))

	if got := servedIDs(t, out); len(got) != 1 || got[0] != "vn-plain" {
		t.Fatalf("routed work query served %v, want only [vn-plain]: a held row must never reach a pool seat (output %q)", got, out)
	}
}

// TestRoutedWorkQueryFallsThroughAWindowOfOnlyHeldRows pins the starvation
// half: when every routed row is held, the tier must report nothing rather than
// exit with rows the hook will strip.
func TestRoutedWorkQueryFallsThroughAWindowOfOnlyHeldRows(t *testing.T) {
	a := holdSquadPoolAgent()
	rows := "[" + holdSquadMemberRow + "," + holdOtherRow + "]"

	out := runShellWithFakeBd(t, a.EffectiveWorkQueryFor(QueryTopology{}), nil, fakeBdRoutedRows(rows))

	if got := strings.TrimSpace(out); got != "[]" {
		t.Fatalf("routed work query over only held rows printed %q, want []", got)
	}
}

func TestPoolDemandCountNeverCountsAHoldSquadMember(t *testing.T) {
	a := holdSquadPoolAgent()
	rows := "[" + holdSquadMemberRow + "," + holdOtherRow + "," + plainSquadLabelRow + "]"

	out := runShellWithFakeBd(t, a.EffectivePoolDemandQueryFor(QueryTopology{}), nil, fakeBdRoutedRows(rows))

	if got := strings.TrimSpace(out); got != "1" {
		t.Fatalf("pool demand count = %q, want 1: a held row is not demand, or the pool spawns a seat that finds nothing", got)
	}
}

// TestRoutedQueriesExcludeHoldSquadAtTheReader pins the bd-side flag: bd reads
// the routed tier through a bounded window, so a held member bd returns takes a
// slot an unheld row needed.
func TestRoutedQueriesExcludeHoldSquadAtTheReader(t *testing.T) {
	a := holdSquadPoolAgent()
	want := `--exclude-label "hold:squad"`
	for name, query := range map[string]string{
		"work query":   a.EffectiveWorkQueryFor(QueryTopology{}),
		"routed pool":  a.EffectiveRoutedPoolQueryFor(QueryTopology{}),
		"pool demand":  a.EffectivePoolDemandQueryFor(QueryTopology{}),
		"federated wq": a.EffectiveWorkQueryFor(QueryTopology{FederatedReady: true}),
	} {
		if !strings.Contains(query, want) {
			t.Errorf("%s does not carry %s: %q", name, want, query)
		}
	}
}
