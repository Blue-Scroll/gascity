package sessionlog

import (
	"reflect"
	"testing"
)

func limitTestIDs(entries []*Entry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.UUID)
	}
	return ids
}

func TestLimitSessionKeepsNewestAndPointsAtTheNextOlderPage(t *testing.T) {
	entries := makeEntries("a", "b", "c", "d", "e")
	sliced, info := sliceAtCompactBoundaries(entries, 0, "", "")
	session := &Session{Messages: sliced, Pagination: info}

	LimitSession(session, 2, false)

	if got := limitTestIDs(session.Messages); !reflect.DeepEqual(got, []string{"d", "e"}) {
		t.Fatalf("messages = %v, want [d e]", got)
	}
	want := PaginationInfo{
		HasOlderMessages:       true,
		TotalMessageCount:      5,
		ReturnedMessageCount:   2,
		TruncatedBeforeMessage: "d",
	}
	if *session.Pagination != want {
		t.Fatalf("pagination = %+v, want %+v", *session.Pagination, want)
	}
}

// The limit cuts after the before cursor, so "load older" pages walk back
// one window at a time with no overlap and no gap.
func TestLimitSessionAfterBeforeCursorReturnsTheWindowJustOlder(t *testing.T) {
	entries := makeEntries("a", "b", "c", "d", "e")
	sliced, info := sliceAtCompactBoundaries(entries, 0, "d", "")
	session := &Session{Messages: sliced, Pagination: info}

	LimitSession(session, 2, false)

	if got := limitTestIDs(session.Messages); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("messages = %v, want [b c]", got)
	}
	if !session.Pagination.HasOlderMessages || !session.Pagination.HasNewerMessages {
		t.Fatalf("pagination = %+v, want older and newer", *session.Pagination)
	}
	if session.Pagination.TruncatedBeforeMessage != "b" {
		t.Fatalf("truncated_before_message = %q, want b", session.Pagination.TruncatedBeforeMessage)
	}
}

// Paging forward keeps the entries right after the cursor. Keeping the newest
// would skip the ones in between and nothing would say so.
func TestLimitSessionAfterCursorKeepsOldestAndReportsNewer(t *testing.T) {
	entries := makeEntries("a", "b", "c", "d", "e")
	sliced, info := sliceAtCompactBoundaries(entries, 0, "", "a")
	session := &Session{Messages: sliced, Pagination: info}

	LimitSession(session, 2, true)

	if got := limitTestIDs(session.Messages); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("messages = %v, want [b c]", got)
	}
	if !session.Pagination.HasNewerMessages || !session.Pagination.HasOlderMessages {
		t.Fatalf("pagination = %+v, want older and newer", *session.Pagination)
	}
	if session.Pagination.ReturnedMessageCount != 2 {
		t.Fatalf("returned = %d, want 2", session.Pagination.ReturnedMessageCount)
	}
}

func TestLimitSessionCutsInsideACompactionSegment(t *testing.T) {
	entries := []*Entry{
		{UUID: "a", Type: "user"},
		{UUID: "cb1", Type: "system", Subtype: "compact_boundary"},
		{UUID: "b", Type: "assistant"},
		{UUID: "c", Type: "user"},
		{UUID: "d", Type: "assistant"},
	}
	// tail=1 returns the whole segment (here the whole log); the limit then
	// cuts inside it.
	sliced, info := sliceAtCompactBoundaries(entries, 1, "", "")
	session := &Session{Messages: sliced, Pagination: info}

	LimitSession(session, 2, false)

	if got := limitTestIDs(session.Messages); !reflect.DeepEqual(got, []string{"c", "d"}) {
		t.Fatalf("messages = %v, want [c d]", got)
	}
	if session.Pagination.TotalCompactions != 1 {
		t.Fatalf("total_compactions = %d, want 1 (counted over the whole log)", session.Pagination.TotalCompactions)
	}
}

func TestLimitSessionLeavesShortOrUnlimitedReadsAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
	}{
		{name: "zero means no limit", limit: 0},
		{name: "negative means no limit", limit: -1},
		{name: "limit equal to length", limit: 3},
		{name: "limit above length", limit: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sliced, info := sliceAtCompactBoundaries(makeEntries("a", "b", "c"), 0, "", "")
			session := &Session{Messages: sliced, Pagination: info}

			LimitSession(session, tc.limit, false)

			if len(session.Messages) != 3 || session.Pagination.HasOlderMessages || session.Pagination.ReturnedMessageCount != 3 {
				t.Fatalf("messages=%v pagination=%+v, want all 3 untouched", limitTestIDs(session.Messages), *session.Pagination)
			}
		})
	}
}

// Some provider readers return no pagination for a whole-file read. A limited
// read must still say that older entries exist.
func TestLimitSessionBuildsPaginationWhenTheReaderGaveNone(t *testing.T) {
	session := &Session{Messages: makeEntries("a", "b", "c")}

	LimitSession(session, 1, false)

	if session.Pagination == nil {
		t.Fatal("pagination = nil, want built")
	}
	want := PaginationInfo{
		HasOlderMessages:       true,
		TotalMessageCount:      3,
		ReturnedMessageCount:   1,
		TruncatedBeforeMessage: "c",
	}
	if *session.Pagination != want {
		t.Fatalf("pagination = %+v, want %+v", *session.Pagination, want)
	}
}
