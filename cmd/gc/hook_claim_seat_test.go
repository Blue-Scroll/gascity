package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// hookClaimSeatFixture is a store checkout plus two seat checkouts beside it,
// all real repositories, because the guard asks git which repository answers
// for a recorded path.
type hookClaimSeatFixture struct {
	store, mine, theirs string
}

func newHookClaimSeatFixture(t *testing.T) hookClaimSeatFixture {
	t.Helper()
	root := t.TempDir()
	f := hookClaimSeatFixture{
		store:  filepath.Join(root, "rig"),
		mine:   filepath.Join(root, "seats", "dune"),
		theirs: filepath.Join(root, "seats", "rock-rider"),
	}
	hookClaimInitRepo(t, f.store, "main")
	hookClaimInitRepo(t, f.mine, "polecat/old-bead-dune")
	hookClaimInitRepo(t, f.theirs, "polecat/vn-s750poz-rock-rider")
	return f
}

// hookClaimSeatRun runs the fresh-claim tier over candidates as the session
// whose checkout is ownDir, and returns the ids it tried to claim, in order.
// Every claim reports a lost race, so the tier tries each candidate it is
// willing to claim and none reaches the receipt path.
func hookClaimSeatRun(t *testing.T, f hookClaimSeatFixture, ownDir string, env []string, candidates []beads.Bead) (tried []string, stderr string) {
	t.Helper()
	opts := hookClaimOptions{
		Assignee:     "vessel-network/dune",
		RouteTargets: []string{"pool"},
		Env:          env,
	}
	ops := hookClaimOps{
		ResolveSessionSeatDir: func(string) string { return ownDir },
		Claim: func(_ context.Context, _ string, _ []string, beadID, _ string) (beads.Bead, bool, error) {
			tried = append(tried, beadID)
			return beads.Bead{}, false, nil
		},
	}
	var out, errOut bytes.Buffer
	result := claimFirstEligibleHookCandidate(candidates, opts, ops, f.store, &out, &errOut)
	if result.terminal {
		t.Fatalf("want a non-terminal result when every claim loses its race, got code=%d stderr=%s", result.code, errOut.String())
	}
	return tried, errOut.String()
}

func hookClaimSeatCandidate(id string, workDirs map[string]string) beads.Bead {
	metadata := map[string]string{beadmeta.RoutedToMetadataKey: "pool"}
	for k, v := range workDirs {
		metadata[k] = v
	}
	return beads.Bead{ID: id, Status: "open", Metadata: metadata}
}

var hookClaimSeatEnv = []string{"GC_SESSION_ID=hq-dune-2"}

// TestClaimFirstEligibleHookCandidateSkipsAnotherSeatsBead pins vn-3qa7dmi: an
// unassigned bead that still records another seat's live checkout is not free,
// under either work-dir key, while a bead that records no checkout is.
func TestClaimFirstEligibleHookCandidateSkipsAnotherSeatsBead(t *testing.T) {
	f := newHookClaimSeatFixture(t)
	for _, key := range []string{beadmeta.WorkDirMetadataKey, beadmeta.LegacyWorkDirMetadataKey} {
		t.Run(key, func(t *testing.T) {
			tried, stderr := hookClaimSeatRun(t, f, f.mine, hookClaimSeatEnv, []beads.Bead{
				hookClaimSeatCandidate("vn-s750poz", map[string]string{key: f.theirs}),
				hookClaimSeatCandidate("vn-free", nil),
			})
			if !slices.Equal(tried, []string{"vn-free"}) {
				t.Fatalf("claims tried = %v, want only [vn-free]", tried)
			}
			if !strings.Contains(stderr, "skipping vn-s750poz") || !strings.Contains(stderr, f.theirs) {
				t.Fatalf("stderr must name the skipped bead and the seat holding it, got %q", stderr)
			}
		})
	}
}

// TestClaimFirstEligibleHookCandidatePutsOwnSeatFirst pins the other half of
// vn-3qa7dmi: the next session of a seat gets back the bead its predecessor
// left in that tree before any other work, even when the query lists it last.
func TestClaimFirstEligibleHookCandidatePutsOwnSeatFirst(t *testing.T) {
	f := newHookClaimSeatFixture(t)
	tried, _ := hookClaimSeatRun(t, f, f.mine, hookClaimSeatEnv, []beads.Bead{
		hookClaimSeatCandidate("vn-first", nil),
		hookClaimSeatCandidate("vn-second", nil),
		hookClaimSeatCandidate("vn-23f5mfu", map[string]string{
			beadmeta.WorkDirMetadataKey:       f.mine,
			beadmeta.LegacyWorkDirMetadataKey: f.mine,
		}),
	})
	if want := []string{"vn-23f5mfu", "vn-first", "vn-second"}; !slices.Equal(tried, want) {
		t.Fatalf("claims tried = %v, want %v", tried, want)
	}
}

// TestClaimFirstEligibleHookCandidateSeatGuardFailsOpen lists every state the
// guard cannot be sure about. Each one must claim exactly as before the guard,
// or a stale path would strand a bead no seat can ever take.
func TestClaimFirstEligibleHookCandidateSeatGuardFailsOpen(t *testing.T) {
	f := newHookClaimSeatFixture(t)
	plain := filepath.Join(t.TempDir(), "artifacts")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		ownDir  string
		env     []string
		workDir string
	}{
		{name: "recorded checkout is gone", ownDir: f.mine, env: hookClaimSeatEnv, workDir: filepath.Join(t.TempDir(), "removed-seat")},
		{name: "recorded checkout is the store", ownDir: f.mine, env: hookClaimSeatEnv, workDir: f.store},
		{name: "recorded path is not a git checkout", ownDir: f.mine, env: hookClaimSeatEnv, workDir: plain},
		{name: "own seat cannot be read", ownDir: "", env: hookClaimSeatEnv, workDir: f.theirs},
		{name: "no session to own a seat", ownDir: f.mine, env: nil, workDir: f.theirs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tried, stderr := hookClaimSeatRun(t, f, tc.ownDir, tc.env, []beads.Bead{
				hookClaimSeatCandidate("vn-x", map[string]string{beadmeta.WorkDirMetadataKey: tc.workDir}),
			})
			if !slices.Equal(tried, []string{"vn-x"}) {
				t.Fatalf("claims tried = %v, want [vn-x]; stderr=%s", tried, stderr)
			}
		})
	}
}

// TestClaimFirstEligibleHookCandidateSeatGuardRunsBeforeReclaim: the opt-in
// stale reclaim reverts a bead to ready before claiming it, so the guard has to
// stand in front of the reclaim too, or the reclaim is the theft.
func TestClaimFirstEligibleHookCandidateSeatGuardRunsBeforeReclaim(t *testing.T) {
	f := newHookClaimSeatFixture(t)
	candidate := hookClaimSeatCandidate("vn-2scy8ez", map[string]string{beadmeta.WorkDirMetadataKey: f.theirs})
	candidate.Assignee = "vessel-network/rock-rider"
	candidate.Status = "in_progress"
	opts := hookClaimOptions{
		Assignee:               "vessel-network/dune",
		RouteTargets:           []string{"pool"},
		Env:                    hookClaimSeatEnv,
		AutoReclaimStaleClaims: true,
	}
	ops := hookClaimOps{
		ResolveSessionSeatDir: func(string) string { return f.mine },
		ReclaimStale: func(_ context.Context, _ string, _ []string, beadID string) (bool, string, error) {
			t.Fatalf("ReclaimStale must not run on a bead another seat's checkout holds, got %s", beadID)
			return false, "", nil
		},
		Claim: func(_ context.Context, _ string, _ []string, beadID, _ string) (beads.Bead, bool, error) {
			t.Fatalf("Claim must not run on a bead another seat's checkout holds, got %s", beadID)
			return beads.Bead{}, false, nil
		},
	}
	var out, errOut bytes.Buffer
	if result := claimFirstEligibleHookCandidate([]beads.Bead{candidate}, opts, ops, f.store, &out, &errOut); result.terminal {
		t.Fatalf("want a non-terminal result, got code=%d stderr=%s", result.code, errOut.String())
	}
}
