package beads

import (
	"context"
	"errors"
	"strings"
	"testing"

	beadslib "github.com/steveyegge/beads"
)

// A bead row and its parent-child edge are two rows. Create used to write them
// as two separate commits, so a process killed in between left a bead with no
// parent edge. That bead is invisible to everything that reaches a step
// through its molecule, so it can never be run and never be swept: 72 of them
// had piled up by 2026-09-20 (vn-g3yqvvk).
//
// No in-process test can kill a process, so these tests pin the property that
// removes the window instead: both rows are written inside ONE transaction,
// and nothing compensates afterwards because there is nothing to compensate.

// createTxSpy records, in order, what Create did and whether a transaction was
// open at the time.
type createTxSpy struct {
	*nativeDoltStorageSpy
	log []string
}

func newCreateTxSpy(addDependencyErr error) *createTxSpy {
	spy := &createTxSpy{}
	spy.nativeDoltStorageSpy = &nativeDoltStorageSpy{
		// Every dependency target resolves, so validation never masks the
		// write ordering this test is about.
		getIssue: func(_ context.Context, id string) (*beadslib.Issue, error) {
			return &beadslib.Issue{ID: id}, nil
		},
		runInTransaction: func(ctx context.Context, commitMsg string, fn func(beadslib.Transaction) error) error {
			spy.log = append(spy.log, "begin "+commitMsg)
			err := fn(nativeDoltTransactionForTest{storage: spy.nativeDoltStorageSpy})
			if err != nil {
				spy.log = append(spy.log, "rollback")
				return err
			}
			spy.log = append(spy.log, "commit")
			return nil
		},
		createIssue: func(_ context.Context, issue *beadslib.Issue, _ string) error {
			spy.log = append(spy.log, "create-issue "+issue.ID)
			return nil
		},
		addDependency: func(_ context.Context, dep *beadslib.Dependency, _ string) error {
			spy.log = append(spy.log, "add-dep "+dep.IssueID+"->"+dep.DependsOnID)
			return addDependencyErr
		},
		removeDependency: func(_ context.Context, issueID, dependsOnID, _ string) error {
			spy.log = append(spy.log, "remove-dep "+issueID+"->"+dependsOnID)
			return nil
		},
		deleteIssue: func(_ context.Context, id string) error {
			spy.log = append(spy.log, "delete-issue "+id)
			return nil
		},
	}
	return spy
}

func TestNativeDoltStoreCreateWritesBeadAndParentEdgeInOneTransaction(t *testing.T) {
	spy := newCreateTxSpy(nil)
	store := newNativeDoltStoreForTest(spy.nativeDoltStorageSpy)

	if _, err := store.Create(Bead{ID: "gc-step", Title: "load-context", ParentID: "gc-molecule"}); err != nil {
		t.Fatalf("Create: got %v, want nil", err)
	}

	want := []string{
		"begin gc: create bead gc-step",
		"create-issue gc-step",
		"add-dep gc-step->gc-molecule",
		"commit",
	}
	if !equalStringSlices(spy.log, want) {
		t.Fatalf("Create wrote:\n  %v\nwant the bead row and its parent edge inside one transaction:\n  %v", spy.log, want)
	}
}

func TestNativeDoltStoreCreateRollsBackInsteadOfCompensating(t *testing.T) {
	writeFailed := errors.New("disk full")
	spy := newCreateTxSpy(writeFailed)
	store := newNativeDoltStoreForTest(spy.nativeDoltStorageSpy)

	_, err := store.Create(Bead{ID: "gc-step", Title: "load-context", ParentID: "gc-molecule"})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Create with a failing edge write: got %v, want the disk-full error", err)
	}

	// The transaction unwrites the bead row. A compensating delete after the
	// commit is exactly the design that could not survive a killed process,
	// so seeing one here means the two-write window is back.
	for _, entry := range spy.log {
		if strings.HasPrefix(entry, "delete-issue ") || strings.HasPrefix(entry, "remove-dep ") {
			t.Fatalf("Create compensated by hand (%q) instead of letting the transaction roll back; full log: %v", entry, spy.log)
		}
	}
	if got := spy.log[len(spy.log)-1]; got != "rollback" {
		t.Fatalf("Create ended with %q, want rollback; full log: %v", got, spy.log)
	}
}

// The store mints the ID when the caller does not pin one, so an edge learns
// which bead it belongs to only after CreateIssue returns. The bead handed
// back must carry the stamped edges, not the blank ones it was built from.
func TestNativeDoltStoreCreateStampsMintedIDOnReturnedDependencies(t *testing.T) {
	spy := newCreateTxSpy(nil)
	spy.nativeDoltStorageSpy.createIssue = func(_ context.Context, issue *beadslib.Issue, _ string) error {
		issue.ID = "gc-minted"
		return nil
	}
	store := newNativeDoltStoreForTest(spy.nativeDoltStorageSpy)

	created, err := store.Create(Bead{Title: "load-context", ParentID: "gc-molecule"})
	if err != nil {
		t.Fatalf("Create: got %v, want nil", err)
	}
	if created.ID != "gc-minted" {
		t.Fatalf("created ID = %q, want gc-minted", created.ID)
	}
	if len(created.Dependencies) != 1 {
		t.Fatalf("created dependencies = %#v, want one parent edge", created.Dependencies)
	}
	if got := created.Dependencies[0].IssueID; got != "gc-minted" {
		t.Fatalf("parent edge IssueID = %q, want gc-minted (the edge was returned unstamped)", got)
	}
	if created.ParentID != "gc-molecule" {
		t.Fatalf("created ParentID = %q, want gc-molecule", created.ParentID)
	}
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
