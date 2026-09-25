package main

import (
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// recordingWorkStore embeds a MemStore (so it satisfies the full beads.Store
// interface) and records every List/Ready query the work-assignment façade
// emits, while delegating to the MemStore for real results. It deliberately
// does NOT implement CachedList or Backing(): the façade's optional-capability
// fast-paths must degrade to the plain List/Ready calls, exactly as a
// non-caching store does today.
type recordingWorkStore struct {
	*beads.MemStore
	listQueries  []beads.ListQuery
	readyQueries []beads.ReadyQuery
}

func newRecordingWorkStore() *recordingWorkStore {
	return &recordingWorkStore{MemStore: beads.NewMemStore()}
}

func (s *recordingWorkStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	s.listQueries = append(s.listQueries, q)
	return s.MemStore.List(q)
}

func (s *recordingWorkStore) Ready(q ...beads.ReadyQuery) ([]beads.Bead, error) {
	if len(q) > 0 {
		s.readyQueries = append(s.readyQueries, q[0])
	} else {
		s.readyQueries = append(s.readyQueries, beads.ReadyQuery{})
	}
	return s.MemStore.Ready(q...)
}

// TestWorkAssignmentOpenAssignedTo_ByteIdenticalQuery asserts the façade's
// OpenAssignedTo emits the exact same ListQuery the raw probe ran:
// {Assignee,Status,Live,TierMode}, against the WORK store.
func TestWorkAssignmentOpenAssignedTo_ByteIdenticalQuery(t *testing.T) {
	rec := newRecordingWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	if _, err := wa.OpenAssignedTo("agent-1", "in_progress", beads.TierBoth, true); err != nil {
		t.Fatalf("OpenAssignedTo: %v", err)
	}

	want := beads.ListQuery{Assignee: "agent-1", Status: "in_progress", Live: true, TierMode: beads.TierBoth}
	if len(rec.listQueries) != 1 {
		t.Fatalf("expected exactly 1 List call, got %d: %#v", len(rec.listQueries), rec.listQueries)
	}
	if !reflect.DeepEqual(rec.listQueries[0], want) {
		t.Fatalf("List query mismatch:\n got  %#v\n want %#v", rec.listQueries[0], want)
	}
}

// TestWorkAssignmentReadyAssignedTo_ByteIdenticalQuery asserts ReadyAssignedTo
// emits the same ReadyQuery{Assignee,TierMode} the raw beads.ReadyLive probe ran.
func TestWorkAssignmentReadyAssignedTo_ByteIdenticalQuery(t *testing.T) {
	rec := newRecordingWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	if _, err := wa.ReadyAssignedTo("agent-2", beads.TierWisps); err != nil {
		t.Fatalf("ReadyAssignedTo: %v", err)
	}

	want := beads.ReadyQuery{Assignee: "agent-2", TierMode: beads.TierWisps}
	if len(rec.readyQueries) != 1 {
		t.Fatalf("expected exactly 1 Ready call, got %d: %#v", len(rec.readyQueries), rec.readyQueries)
	}
	if !reflect.DeepEqual(rec.readyQueries[0], want) {
		t.Fatalf("Ready query mismatch:\n got  %#v\n want %#v", rec.readyQueries[0], want)
	}
}

// TestWorkAssignmentCachedOpenAssignedWisps_NoFastPathWithoutCache asserts that
// on a store without the CachedList capability the cache probe reports "not
// answered" (so the caller falls through to OpenAssignedTo), and crucially does
// NOT assert CachedList on the WorkStore wrapper (which would always fail and
// silently drop the cache on a real caching store).
func TestWorkAssignmentCachedOpenAssignedWisps_NoFastPathWithoutCache(t *testing.T) {
	rec := newRecordingWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	if items, ok := wa.CachedOpenAssignedWisps("agent-3", "open"); ok {
		t.Fatalf("expected cache miss on non-caching store, got ok=true items=%#v", items)
	}
	if len(rec.listQueries) != 0 {
		t.Fatalf("cache probe must not issue a List on a non-caching store, got %#v", rec.listQueries)
	}
}

// fakeCachingWorkStore implements the CachedList capability on the underlying
// store (not the wrapper) so the façade's fast-path can be exercised.
type fakeCachingWorkStore struct {
	*beads.MemStore
	cachedCalls []beads.ListQuery
	cachedHit   []beads.Bead
	cachedOK    bool
}

func (s *fakeCachingWorkStore) CachedList(q beads.ListQuery) ([]beads.Bead, bool) {
	s.cachedCalls = append(s.cachedCalls, q)
	return s.cachedHit, s.cachedOK
}

// TestWorkAssignmentCachedOpenAssignedWisps_UsesUnwrappedStore proves the
// CachedList assertion is made on the embedded .Store, not the WorkStore
// wrapper: the wrapper does not promote CachedList, so asserting on it would
// miss this capability and silently lose the fast-path (the typed-nil trap).
func TestWorkAssignmentCachedOpenAssignedWisps_UsesUnwrappedStore(t *testing.T) {
	want := beads.ListQuery{Assignee: "agent-4", Status: "in_progress", TierMode: beads.TierWisps}
	cache := &fakeCachingWorkStore{
		MemStore:  beads.NewMemStore(),
		cachedHit: []beads.Bead{{ID: "w-1"}},
		cachedOK:  true,
	}
	wa := workAssignmentForStore(beads.WorkStore{Store: cache})

	items, ok := wa.CachedOpenAssignedWisps("agent-4", "in_progress")
	if !ok {
		t.Fatalf("expected cache hit via unwrapped .Store, got ok=false")
	}
	if len(items) != 1 || items[0].ID != "w-1" {
		t.Fatalf("unexpected cached items: %#v", items)
	}
	if len(cache.cachedCalls) != 1 || !reflect.DeepEqual(cache.cachedCalls[0], want) {
		t.Fatalf("CachedList query mismatch:\n got %#v\n want %#v", cache.cachedCalls, want)
	}
}

// TestWorkAssignmentForStore_NilUnderlyingStoreSafe asserts the façade tolerates
// a nil underlying store the same way the raw probes did (return empty, no
// panic).
func TestWorkAssignmentForStore_NilUnderlyingStoreSafe(t *testing.T) {
	wa := workAssignmentForStore(beads.WorkStore{Store: nil})
	if items, err := wa.OpenAssignedTo("a", "open", beads.TierBoth, true); err != nil || items != nil {
		t.Fatalf("nil store OpenAssignedTo: items=%#v err=%v", items, err)
	}
	if items, err := wa.ReadyAssignedTo("a", beads.TierIssues); err != nil || items != nil {
		t.Fatalf("nil store ReadyAssignedTo: items=%#v err=%v", items, err)
	}
	if items, ok := wa.CachedOpenAssignedWisps("a", "open"); ok || items != nil {
		t.Fatalf("nil store CachedOpenAssignedWisps: items=%#v ok=%v", items, ok)
	}
}

// TestWorkAssignmentAssignedToInStatuses_OneLiveBothTierRead pins the cost and
// the answer of the read behind every "does this session still hold work?"
// probe: ONE live TierBoth List with no status, and the status match done in
// Go, so an issue and a wisp in either wanted status both come back while a
// blocked bead and another identity's bead do not.
func TestWorkAssignmentAssignedToInStatuses_OneLiveBothTierRead(t *testing.T) {
	rec := newRecordingWorkStore()
	mustCreate := func(b beads.Bead) string {
		t.Helper()
		return createWorkBeadWithStatus(t, rec.MemStore, b)
	}
	openIssue := mustCreate(beads.Bead{Title: "open issue", Type: "task", Status: "open", Assignee: "sess-1"})
	activeWisp := mustCreate(beads.Bead{Title: "active wisp", Type: "task", Status: "in_progress", Assignee: "sess-1", Ephemeral: true})
	mustCreate(beads.Bead{Title: "blocked issue", Type: "task", Status: "blocked", Assignee: "sess-1"})
	mustCreate(beads.Bead{Title: "someone else", Type: "task", Status: "open", Assignee: "sess-2"})

	wa := workAssignmentForStore(beads.WorkStore{Store: rec})
	items, err := wa.AssignedToInStatuses("sess-1", []string{"open", "in_progress"})
	if err != nil {
		t.Fatalf("AssignedToInStatuses: %v", err)
	}

	want := beads.ListQuery{Assignee: "sess-1", Live: true, TierMode: beads.TierBoth}
	if len(rec.listQueries) != 1 || !reflect.DeepEqual(rec.listQueries[0], want) {
		t.Fatalf("List queries = %#v, want exactly one %#v", rec.listQueries, want)
	}
	got := map[string]bool{}
	for _, item := range items {
		got[item.ID] = true
	}
	if len(got) != 2 || !got[openIssue] || !got[activeWisp] {
		t.Fatalf("AssignedToInStatuses ids = %v, want exactly %s and %s", got, openIssue, activeWisp)
	}
}

// TestSessionAssignedWorkProbes_OneReadPerIdentity pins the fan-out of both
// store probes: one read per distinct identity, whatever the statuses. Before
// hq-ltq0n7 each probe read every (status, tier) pair on its own, which on a
// bd store is 6 subprocesses per identity per store instead of 2, and the
// drain-ack finalize ran that up to three times per session.
func TestSessionAssignedWorkProbes_OneReadPerIdentity(t *testing.T) {
	identifiers := []string{"sess-1", "", "sess-1", "polecat-a"}
	statuses := []string{"open", "in_progress"}
	probes := map[string]func(beads.Store, []string, []string) (bool, error){
		"general":    sessionHasAssignedWorkInStoreByIdentifiersForStatuses,
		"close gate": sessionHasAssignedWorkInStoreByIdentifiersForStatusesForCloseGate,
	}
	for name, probe := range probes {
		t.Run(name+"/no work reads each identity once", func(t *testing.T) {
			rec := newRecordingWorkStore()
			has, err := probe(rec, identifiers, statuses)
			if err != nil || has {
				t.Fatalf("probe = %v, %v; want false, nil", has, err)
			}
			if len(rec.listQueries) != 2 {
				t.Fatalf("List calls = %d (%#v), want 2: one per distinct identity", len(rec.listQueries), rec.listQueries)
			}
		})
		t.Run(name+"/finds in_progress wisp work", func(t *testing.T) {
			rec := newRecordingWorkStore()
			createWorkBeadWithStatus(t, rec.MemStore, beads.Bead{Title: "wisp", Type: "task", Status: "in_progress", Assignee: "polecat-a", Ephemeral: true})
			has, err := probe(rec, identifiers, statuses)
			if err != nil || !has {
				t.Fatalf("probe = %v, %v; want true, nil", has, err)
			}
		})
		t.Run(name+"/ignores a status it was not asked about", func(t *testing.T) {
			rec := newRecordingWorkStore()
			createWorkBeadWithStatus(t, rec.MemStore, beads.Bead{Title: "open", Type: "task", Status: "open", Assignee: "sess-1"})
			has, err := probe(rec, identifiers, []string{"in_progress"})
			if err != nil || has {
				t.Fatalf("probe = %v, %v; want false, nil for open work under an in_progress-only probe", has, err)
			}
		})
	}
}

// createWorkBeadWithStatus creates b and then sets its status, because
// MemStore.Create always stores a new bead as "open" whatever b.Status says.
// Without the second write a fixture's status is silently wrong.
func createWorkBeadWithStatus(t *testing.T, store *beads.MemStore, b beads.Bead) string {
	t.Helper()
	created, err := store.Create(b)
	if err != nil {
		t.Fatalf("Create(%s): %v", b.Title, err)
	}
	if b.Status != "" && created.Status != b.Status {
		status := b.Status
		if err := store.Update(created.ID, beads.UpdateOpts{Status: &status}); err != nil {
			t.Fatalf("Update(%s, status=%s): %v", created.ID, status, err)
		}
	}
	got, err := store.Get(created.ID)
	if err != nil || (b.Status != "" && got.Status != b.Status) {
		t.Fatalf("fixture %s: status = %q, err = %v; want %q", created.ID, got.Status, err, b.Status)
	}
	return created.ID
}
