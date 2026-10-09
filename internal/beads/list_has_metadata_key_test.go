package beads

import (
	"strings"
	"testing"
)

// HasMetadataKey lets a caller read the few rows that carry a marker instead
// of every row of a type (vn-d5jn83b). These tests pin it on each backend that
// had to learn it: pushed down where the backend can, cut by Matches where it
// cannot, and never under a backend limit taken before that cut.

func TestListQueryHasMetadataKeyIsAFilterAndMatchesPresence(t *testing.T) {
	q := ListQuery{HasMetadataKey: "configured_named_identity", IncludeClosed: true}
	if !q.HasFilter() {
		t.Fatal("HasFilter() = false, want true for HasMetadataKey")
	}
	if !q.Matches(Bead{Status: "closed", Metadata: map[string]string{"configured_named_identity": "rig/refinery"}}) {
		t.Error("Matches() = false for a bead carrying the key")
	}
	if !q.Matches(Bead{Status: "closed", Metadata: map[string]string{"configured_named_identity": ""}}) {
		t.Error("Matches() = false for a bead carrying the key with an empty value; the key is present")
	}
	if q.Matches(Bead{Status: "closed", Metadata: map[string]string{"session_name": "pool-worker"}}) {
		t.Error("Matches() = true for a bead without the key")
	}
	if q.Matches(Bead{Status: "closed"}) {
		t.Error("Matches() = true for a bead with no metadata")
	}
}

func TestMemStoreListHasMetadataKey(t *testing.T) {
	m := NewMemStore()
	keep, err := m.Create(Bead{Title: "named", Metadata: map[string]string{"configured_named_identity": "rig/refinery"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(Bead{Title: "pool", Metadata: map[string]string{"session_name": "pool-worker"}}); err != nil {
		t.Fatal(err)
	}
	got, err := m.List(ListQuery{HasMetadataKey: "configured_named_identity"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != keep.ID {
		t.Fatalf("List = %v, want only %s", got, keep.ID)
	}
}

func TestNativeIssueFilterPushesHasMetadataKey(t *testing.T) {
	filter := nativeIssueFilterFromListQuery(ListQuery{HasMetadataKey: "configured_named_identity", IncludeClosed: true})
	if filter.HasMetadataKey != "configured_named_identity" {
		t.Fatalf("HasMetadataKey = %q, want configured_named_identity", filter.HasMetadataKey)
	}
}

func TestBdStoreListPushesHasMetadataKeyAndFetchesUnbounded(t *testing.T) {
	var gotArgs []string
	runner := func(_, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`[]`), nil
	}
	s := NewBdStore("/city", runner)
	if _, err := s.List(ListQuery{HasMetadataKey: "configured_named_identity", IncludeClosed: true, Limit: 5, Sort: SortCreatedDesc}); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(gotArgs, " ")
	if !strings.Contains(args, "--has-metadata-key configured_named_identity") {
		t.Fatalf("args = %q, want --has-metadata-key configured_named_identity", args)
	}
	if !strings.Contains(args, "--limit 0") {
		t.Fatalf("args = %q, want --limit 0: the key is cut again Go-side, so bd must not cut first", args)
	}
	if !canApplyWispsServerLimit(ListQuery{Limit: 5}) || canApplyWispsServerLimit(ListQuery{Limit: 5, HasMetadataKey: "k"}) {
		t.Fatal("canApplyWispsServerLimit must refuse a HasMetadataKey query; the wisp bd query has no such flag")
	}
}

func TestSQLiteListHasMetadataKeyDrivesOffMetadataIndex(t *testing.T) {
	s := newSQLiteGraphApplyStore(t, t.TempDir(), WithSQLiteStoreIDPrefix(sqliteGraphPrefix))
	q := ListQuery{HasMetadataKey: "configured_named_identity", IncludeClosed: true, Sort: SortCreatedDesc}

	sqlText, args := sqliteListSQL(q, "b.id")
	rows, err := s.readDB.Query("EXPLAIN QUERY PLAN "+sqlText, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", sqlText, err)
	}
	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, detail)
	}
	rows.Close() //nolint:errcheck
	if joined := strings.Join(plan, " | "); !strings.Contains(joined, "idx_metadata_key_value") {
		t.Fatalf("has-key list does not use the metadata index:\n  sql: %s\n  plan: %s", sqlText, joined)
	}

	keep, err := s.Create(Bead{Title: "named", Type: "session", Metadata: map[string]string{"configured_named_identity": "rig/refinery"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Close(keep.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Create(Bead{Title: "pool", Type: "session", Metadata: map[string]string{"session_name": "pool-worker"}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.List(q)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != keep.ID {
		t.Fatalf("List = %v, want only %s", got, keep.ID)
	}
}
