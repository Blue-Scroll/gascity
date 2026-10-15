package beads

import (
	"context"
	"fmt"
	"testing"

	beadslib "github.com/steveyegge/beads"
)

// A formula pour creates ~30 beads one at a time, each with its edges. The
// library checks every edge for a cycle with a recursive query over both
// dependency tables, and under load one of those queries outran the client's
// socket timeout and rolled the whole pour back: 7 pours of 7 on 2026-10-15
// (vn-5i9cwlv). An edge out of a bead the same transaction just minted cannot
// close a cycle, so Create skips the check for exactly those edges. These
// tests pin where the line falls.

// cycleCheckSpy records each edge Create writes and whether it asked the
// library to skip the cycle check.
type cycleCheckSpy struct {
	*nativeDoltStorageSpy
	edges []string
}

func (s *cycleCheckSpy) recordDependencyAddOptions(dep *beadslib.Dependency, opts beadslib.DependencyAddOptions) {
	s.edges = append(s.edges, fmt.Sprintf("%s->%s skip=%t", dep.IssueID, dep.DependsOnID, opts.SkipCycleCheck))
}

// newCycleCheckSpy mints mintedID for a bead created without a pinned id.
func newCycleCheckSpy(mintedID string) *cycleCheckSpy {
	spy := &cycleCheckSpy{}
	spy.nativeDoltStorageSpy = &nativeDoltStorageSpy{
		getIssue: func(_ context.Context, id string) (*beadslib.Issue, error) {
			return &beadslib.Issue{ID: id}, nil
		},
		runInTransaction: func(_ context.Context, _ string, fn func(beadslib.Transaction) error) error {
			return fn(nativeDoltTransactionForTest{storage: spy})
		},
		createIssue: func(_ context.Context, issue *beadslib.Issue, _ string) error {
			if issue.ID == "" {
				issue.ID = mintedID
			}
			return nil
		},
		addDependency: func(context.Context, *beadslib.Dependency, string) error {
			return nil
		},
	}
	return spy
}

func TestNativeDoltStoreCreateSkipsCycleCheckOnEdgesOutOfAMintedBead(t *testing.T) {
	spy := newCycleCheckSpy("gc-minted")
	store := newNativeDoltStoreForTest(spy)

	_, err := store.Create(Bead{
		Title:    "plan-review-2",
		ParentID: "gc-molecule",
		Needs:    []string{"gc-plan-review-1", "conditional-blocks:gc-gate"},
	})
	if err != nil {
		t.Fatalf("Create: got %v, want nil", err)
	}

	want := []string{
		"gc-minted->gc-molecule skip=true",
		"gc-minted->gc-plan-review-1 skip=true",
		"gc-minted->gc-gate skip=true",
	}
	if !equalStringSlices(spy.edges, want) {
		t.Fatalf("Create wrote edges:\n  %v\nwant every edge out of the minted bead to skip the cycle check:\n  %v", spy.edges, want)
	}
}

// A pinned id may already be named by edges in the ledger (a migration copy
// lands beads in an order it does not control), so the cycle check stays.
func TestNativeDoltStoreCreateKeepsCycleCheckForAPinnedID(t *testing.T) {
	spy := newCycleCheckSpy("gc-unused")
	store := newNativeDoltStoreForTest(spy)

	_, err := store.Create(Bead{
		ID:           "gc-pinned",
		Title:        "copied",
		ParentID:     "gc-molecule",
		Needs:        []string{"gc-other"},
		Dependencies: []Dep{{DependsOnID: "gc-target", Type: "blocks"}},
	})
	if err != nil {
		t.Fatalf("Create: got %v, want nil", err)
	}

	want := []string{
		"gc-pinned->gc-target skip=false",
		"gc-pinned->gc-molecule skip=false",
		"gc-pinned->gc-other skip=false",
	}
	if !equalStringSlices(spy.edges, want) {
		t.Fatalf("Create wrote edges:\n  %v\nwant a pinned id's edges checked for a cycle:\n  %v", spy.edges, want)
	}
}

// An edge the caller gave another source is an edge between two beads that
// already exist, which can close a cycle, so it keeps the check even when the
// bead being created was minted.
func TestNativeDoltStoreCreateKeepsCycleCheckForAnEdgeWithAnotherSource(t *testing.T) {
	spy := newCycleCheckSpy("gc-minted")
	store := newNativeDoltStoreForTest(spy)

	_, err := store.Create(Bead{
		Title: "step",
		Dependencies: []Dep{
			{DependsOnID: "gc-target", Type: "blocks"},
			{IssueID: "gc-elsewhere", DependsOnID: "gc-target", Type: "blocks"},
		},
	})
	if err != nil {
		t.Fatalf("Create: got %v, want nil", err)
	}

	want := []string{
		"gc-minted->gc-target skip=true",
		"gc-elsewhere->gc-target skip=false",
	}
	if !equalStringSlices(spy.edges, want) {
		t.Fatalf("Create wrote edges:\n  %v\nwant only the minted bead's own edge to skip the cycle check:\n  %v", spy.edges, want)
	}
}
