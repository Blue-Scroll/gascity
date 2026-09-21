package sling

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// countingRunner is a SlingRunner that is safe to call from several goroutines.
// newFakeRunner's appends are not, and these tests race slings on purpose.
type countingRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *countingRunner) run(_, _ string, _ map[string]string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return "", nil
}

// openMoleculeRoots returns every open molecule root in the store.
func openMoleculeRoots(t *testing.T, store beads.Store) []beads.Bead {
	t.Helper()
	items, err := store.List(beads.ListQuery{AllowScan: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var roots []beads.Bead
	for _, item := range items {
		if IsMoleculeAttachment(item) && item.Status != "closed" {
			roots = append(roots, item)
		}
	}
	return roots
}

// legacyAttachRaceDeps builds one shared store, city path and work bead for a
// race between several slings of the same legacy (v1) formula on one bead.
func legacyAttachRaceDeps(t *testing.T) (SlingDeps, beads.Bead, config.Agent, *countingRunner) {
	t.Helper()
	runner := &countingRunner{}
	cfg := &config.City{Workspace: config.Workspace{Name: "test"}}
	deps := testDeps(cfg, runtime.NewFake(), runner.run)
	// Own city path: the lock file lives under it, and the shared test city
	// dir would let an unrelated test's lock decide this one.
	deps.CityPath = t.TempDir()
	bead, err := deps.Store.Create(beads.Bead{Title: "work", Type: "task", Status: "open"})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}
	return deps, bead, config.Agent{Name: "mayor", MaxActiveSessions: intPtr(1)}, runner
}

// TestLegacyAttachRaceLeavesOneMoleculeRoot is the regression test for
// vn-7cw0yut: several routers slinging the same bead at the same time must
// leave exactly ONE open molecule root, never one per racer.
//
// Without the per-source-bead lock this fails with one root per goroutine. The
// "is a molecule already attached?" check runs before InstantiateSlingFormula,
// and the bead's molecule_id is written after it, so every racer reads the bead
// as free and builds its own. In the wild that window was over two minutes wide
// (vn-5ogw5pv, 2026-09-20: roots vn-3w1trsn at 13:06:54Z and vn-0iqcywg at
// 13:09:08Z, plus 11 stray open beads for the loser).
func TestLegacyAttachRaceLeavesOneMoleculeRoot(t *testing.T) {
	deps, bead, agent, _ := legacyAttachRaceDeps(t)

	const racers = 6
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = DoSling(SlingOpts{
				Target:        agent,
				BeadOrFormula: bead.ID,
				OnFormula:     "mol-polecat-work",
			}, deps, deps.Store)
		}(i)
	}
	wg.Wait()

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		}
	}
	if won == 0 {
		t.Fatalf("every sling failed, so the test proves nothing; errs = %v", errs)
	}

	roots := openMoleculeRoots(t, deps.Store)
	if len(roots) != 1 {
		ids := make([]string, 0, len(roots))
		for _, root := range roots {
			ids = append(ids, root.ID)
		}
		t.Fatalf("open molecule roots = %d %v, want exactly 1: a second router built a second molecule for %s", len(roots), ids, bead.ID)
	}

	after, err := deps.Store.Get(bead.ID)
	if err != nil {
		t.Fatalf("Get(%s): %v", bead.ID, err)
	}
	if got := after.Metadata["molecule_id"]; got != roots[0].ID {
		t.Fatalf("molecule_id on %s = %q, want the one live root %q", bead.ID, got, roots[0].ID)
	}
}

// TestLegacyAttachTakesCrossProcessFileLock proves the guard is the on-disk
// sourceworkflow lock and not a process-local mutex. Two `gc sling` processes
// on this Mac are the case that was measured, and only a file lock covers them.
func TestLegacyAttachTakesCrossProcessFileLock(t *testing.T) {
	deps, bead, agent, _ := legacyAttachRaceDeps(t)

	if _, err := DoSling(SlingOpts{
		Target:        agent,
		BeadOrFormula: bead.ID,
		OnFormula:     "mol-polecat-work",
	}, deps, deps.Store); err != nil {
		t.Fatalf("DoSling: %v", err)
	}

	lockDir := filepath.Join(citylayout.RuntimeDataDir(deps.CityPath), "sling-source-locks")
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		t.Fatalf("reading %s (a legacy attach must take a cross-process file lock on the source bead): %v", lockDir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("%s is empty; the attach took no cross-process file lock, so a second gc process can still build a second molecule", lockDir)
	}
}

// TestLegacyAttachReplacesAnAlreadyAttachedMoleculeInPlace pins what the loser
// of the race actually does once the lock makes it see the winner's molecule.
//
// It does NOT refuse. checkNoMoleculeChildren auto-burns a live molecule when
// the source bead is unassigned, which is every pool bead, so the second sling
// closes the first molecule and pours its own. That is the designed re-sling
// behaviour and the lock deliberately does not change it. What the lock does
// change is the count: exactly one molecule root stays open, and the bead's
// molecule_id names it. Before the lock, both roots stayed open because the
// loser's check ran while molecule_id was still empty and found nothing to burn.
func TestLegacyAttachReplacesAnAlreadyAttachedMoleculeInPlace(t *testing.T) {
	deps, bead, agent, _ := legacyAttachRaceDeps(t)
	opts := SlingOpts{Target: agent, BeadOrFormula: bead.ID, OnFormula: "mol-polecat-work"}

	first, err := DoSling(opts, deps, deps.Store)
	if err != nil {
		t.Fatalf("first DoSling: %v", err)
	}
	if first.WispRootID == "" {
		t.Fatal("first sling attached no molecule")
	}

	// Clear the route so preflight's idempotent short-circuit cannot be what
	// handles this. The molecule check inside the lock has to.
	if err := deps.Store.SetMetadata(bead.ID, "gc.routed_to", ""); err != nil {
		t.Fatalf("clearing gc.routed_to: %v", err)
	}

	second, err := DoSling(opts, deps, deps.Store)
	if err != nil {
		t.Fatalf("second DoSling: %v", err)
	}

	roots := openMoleculeRoots(t, deps.Store)
	if len(roots) != 1 {
		t.Fatalf("open molecule roots = %d, want exactly 1 after a re-sling", len(roots))
	}
	if roots[0].ID == first.WispRootID {
		t.Fatalf("re-sling kept the first molecule %s open; it should have been burned and replaced", first.WispRootID)
	}
	if second.WispRootID != roots[0].ID {
		t.Fatalf("second sling reported root %q, want the one live root %q", second.WispRootID, roots[0].ID)
	}

	after, err := deps.Store.Get(bead.ID)
	if err != nil {
		t.Fatalf("Get(%s): %v", bead.ID, err)
	}
	if got := after.Metadata["molecule_id"]; got != roots[0].ID {
		t.Fatalf("molecule_id on %s = %q, want the one live root %q", bead.ID, got, roots[0].ID)
	}
}

// legacyBatchAttachRaceDeps builds one shared store, city path and convoy with
// childCount open children, ready for a race between several DoSlingBatch calls
// on that one convoy.
func legacyBatchAttachRaceDeps(t *testing.T, childCount int) (SlingDeps, beads.Bead, []beads.Bead, config.Agent) {
	t.Helper()
	runner := &countingRunner{}
	cfg := &config.City{Workspace: config.Workspace{Name: "test"}}
	deps := testDeps(cfg, runtime.NewFake(), runner.run)
	// Own city path, for the same reason as legacyAttachRaceDeps: the lock
	// file lives under it, and the shared test city dir would let an
	// unrelated test's lock decide this one.
	deps.CityPath = t.TempDir()
	convoy, err := deps.Store.Create(beads.Bead{Title: "convoy", Type: "convoy"})
	if err != nil {
		t.Fatalf("create convoy: %v", err)
	}
	children := make([]beads.Bead, 0, childCount)
	for i := range childCount {
		child, err := deps.Store.Create(beads.Bead{Title: fmt.Sprintf("child %d", i), Type: "task", Status: "open"})
		if err != nil {
			t.Fatalf("create child %d: %v", i, err)
		}
		if err := deps.Store.DepAdd(convoy.ID, child.ID, "tracks"); err != nil {
			t.Fatalf("track child %d: %v", i, err)
		}
		children = append(children, child)
	}
	return deps, convoy, children, config.Agent{Name: "mayor", MaxActiveSessions: intPtr(1)}
}

// TestLegacyBatchAttachRaceLeavesOneMoleculeRootPerChild is the convoy twin of
// TestLegacyAttachRaceLeavesOneMoleculeRoot, and the regression test for
// vn-w7bhfu2: several routers expanding the SAME convoy at the same time must
// leave exactly ONE open molecule root per child, never one per racer per child.
//
// The batch path's gap was wider than the single-bead one. Its "is a molecule
// already attached?" check, CheckBatchNoMoleculeChildren, runs once in
// DoSlingBatch before any child is touched, off a snapshot of the children
// taken before that. Each child then only gets its molecule_id at the end of
// its own attach, so every child read free from that one batch check until its
// own write, and both routers built a full molecule for every child.
//
// Without the per-child lock this fails with up to racers*children open roots.
func TestLegacyBatchAttachRaceLeavesOneMoleculeRootPerChild(t *testing.T) {
	const childCount = 3
	deps, convoy, children, agent := legacyBatchAttachRaceDeps(t, childCount)

	const racers = 4
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = DoSlingBatch(SlingOpts{
				Target:        agent,
				BeadOrFormula: convoy.ID,
				OnFormula:     "mol-polecat-work",
			}, deps, deps.Store)
		}(i)
	}
	wg.Wait()

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		}
	}
	if won == 0 {
		t.Fatalf("every batch sling failed, so the test proves nothing; errs = %v", errs)
	}

	roots := openMoleculeRoots(t, deps.Store)
	if len(roots) != childCount {
		ids := make([]string, 0, len(roots))
		for _, root := range roots {
			ids = append(ids, root.ID)
		}
		t.Fatalf("open molecule roots = %d %v, want exactly %d (one per child of %s): a second router built a second molecule for at least one child", len(roots), ids, childCount, convoy.ID)
	}

	live := make(map[string]bool, len(roots))
	for _, root := range roots {
		live[root.ID] = true
	}
	for _, child := range children {
		after, err := deps.Store.Get(child.ID)
		if err != nil {
			t.Fatalf("Get(%s): %v", child.ID, err)
		}
		got := after.Metadata["molecule_id"]
		if got == "" {
			t.Fatalf("molecule_id on child %s is empty; it was never attached", child.ID)
		}
		if !live[got] {
			t.Fatalf("molecule_id on child %s = %q, which is not one of the open roots %v: the child points at a burned molecule", child.ID, got, roots)
		}
		delete(live, got)
	}
	if len(live) != 0 {
		t.Fatalf("open molecule roots %v belong to no child of %s; they are orphans a losing router left behind", live, convoy.ID)
	}
}
