package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/sessionlog"
	"github.com/gastownhall/gascity/internal/worker"
)

func TestBoundedStructuredResumeTokenResumesLikeTheWholeToken(t *testing.T) {
	whole := testStructuredStreamProjection("stream-a", testStructuredMessages(0, 100)...)
	token, ok := boundedStructuredResumeToken(whole, false)
	if !ok {
		t.Fatal("bounded token not made for a projection with messages")
	}
	decoded, ok := decodeStructuredResumeToken(token)
	if !ok || !decoded.SuffixWindow || decoded.MessageCount != structuredStreamTokenWindow || decoded.AfterEntryID != "m99" {
		t.Fatalf("bounded token = %+v (valid %t), want a %d-message suffix window ending at m99", decoded, ok, structuredStreamTokenWindow)
	}

	if update := buildStructuredStreamUpdate(token, whole, false); update != nil {
		t.Fatalf("unchanged whole read = %+v, want nil", update)
	}

	grown := testStructuredStreamProjection("stream-a", testStructuredMessages(0, 102)...)
	update := buildStructuredStreamUpdate(token, grown, false)
	if update == nil || update.Operation != sessionStructuredOperationUpsert {
		t.Fatalf("grown whole read = %+v, want upsert", update)
	}
	if got := structuredMessageIDs(update.StructuredMessages); !equalStrings(got, []string{"m99", "m100", "m101"}) {
		t.Fatalf("upsert IDs = %v, want inclusive tail [m99 m100 m101]", got)
	}

	// The end of the log alone answers the same token the same way.
	end := testStructuredStreamProjection("stream-a", testStructuredMessages(50, 102)...)
	end.Pagination = &sessionlog.PaginationInfo{HasOlderMessages: true, PartialRead: true}
	update = buildStructuredStreamUpdate(token, end, false)
	if update == nil || update.Operation != sessionStructuredOperationUpsert {
		t.Fatalf("grown end of log = %+v, want upsert", update)
	}
	if got := structuredMessageIDs(update.StructuredMessages); !equalStrings(got, []string{"m99", "m100", "m101"}) {
		t.Fatalf("upsert IDs = %v, want inclusive tail [m99 m100 m101]", got)
	}

	rewritten := testStructuredStreamProjection("stream-a", testStructuredMessages(0, 100)...)
	rewritten.StructuredMessages[90] = testStructuredMessageWithText("m90", "final", "edited")
	assertStructuredReset(t, buildStructuredStreamUpdate(token, rewritten, false), sessionStructuredResetHistoryRewritten, structuredMessageIDs(rewritten.StructuredMessages))
}

func TestBoundedStructuredResumeTokenNeedsAMessage(t *testing.T) {
	if token, ok := boundedStructuredResumeToken(testStructuredStreamProjection("stream-a"), false); ok {
		t.Fatalf("bounded token for an empty projection = %q, want none", token)
	}
}

func TestLiveStructuredStreamUpdateBoundsTheTokenItSends(t *testing.T) {
	whole := testStructuredStreamProjection("stream-a", testStructuredMessages(0, 100)...)
	update, next := liveStructuredStreamUpdate("", whole, false)
	if update == nil || update.Operation != sessionStructuredOperationSnapshot || len(update.StructuredMessages) != 100 {
		t.Fatalf("first update = %+v, want the whole snapshot", update)
	}
	if update.History.Cursor.ResumeToken != next {
		t.Fatalf("frame token %q != stream token %q", update.History.Cursor.ResumeToken, next)
	}
	if decoded, ok := decodeStructuredResumeToken(next); !ok || decoded.MessageCount != structuredStreamTokenWindow {
		t.Fatalf("stream token = %+v (valid %t), want bounded", decoded, ok)
	}

	// A whole token from a REST read that the stream has nothing to add to is
	// still swapped for a bounded one, so the next reload can read the end.
	restToken := buildStructuredStreamUpdate("", whole, false).History.Cursor.ResumeToken
	update, next = liveStructuredStreamUpdate(restToken, whole, false)
	if update != nil {
		t.Fatalf("exact resume update = %+v, want nil", update)
	}
	if decoded, ok := decodeStructuredResumeToken(next); !ok || !decoded.SuffixWindow {
		t.Fatalf("stream token after exact resume = %+v (valid %t), want a suffix window", decoded, ok)
	}
}

func TestStructuredStreamReloadReadsNothingWhenTheLogIsUnchanged(t *testing.T) {
	h := newReloadTestHandle(t, 5)
	initial := h.wholeSnapshot(t)
	reload := newStructuredStreamReload(h, false, initial)
	h.takeRequests()

	snapshot, err := reload.read(context.Background(), "")
	if err != nil || snapshot != nil {
		t.Fatalf("read of an unchanged log = %+v, %v; want nil, nil", snapshot, err)
	}
	if got := h.takeRequests(); len(got) != 0 {
		t.Fatalf("history requests for an unchanged log = %+v, want none", got)
	}

	h.appendEntries(t, 1)
	snapshot, err = reload.read(context.Background(), "")
	if err != nil || snapshot == nil {
		t.Fatalf("read after a write = %+v, %v; want a snapshot", snapshot, err)
	}
	if got := h.takeRequests(); len(got) != 1 {
		t.Fatalf("history requests after a write = %+v, want one", got)
	}
	if snapshot, err = reload.read(context.Background(), ""); err != nil || snapshot != nil {
		t.Fatalf("second read of the same write = %+v, %v; want nil, nil", snapshot, err)
	}
}

func TestStructuredStreamReloadReadsTheEndOfTheLog(t *testing.T) {
	h := newReloadTestHandle(t, 500)
	initial := h.wholeSnapshot(t)
	_, token := liveStructuredStreamUpdate("", h.projection(initial), false)
	reload := newStructuredStreamReload(h, false, initial)
	h.takeRequests()

	h.appendEntries(t, 3)
	snapshot, err := reload.read(context.Background(), token)
	if err != nil || snapshot == nil {
		t.Fatalf("read after a write = %+v, %v; want a snapshot", snapshot, err)
	}
	wantLimit := structuredStreamTokenWindow + structuredStreamReloadSlack
	if got := h.takeRequests(); len(got) != 1 || got[0].Limit != wantLimit {
		t.Fatalf("history requests = %+v, want one read of the last %d entries", got, wantLimit)
	}
	update, _ := liveStructuredStreamUpdate(token, h.projection(snapshot), false)
	if update == nil || update.Operation != sessionStructuredOperationUpsert {
		t.Fatalf("update = %+v, want upsert", update)
	}
	if got := structuredMessageIDs(update.StructuredMessages); !equalStrings(got, []string{"m499", "m500", "m501", "m502"}) {
		t.Fatalf("upsert IDs = %v, want inclusive tail [m499 .. m502]", got)
	}
}

func TestStructuredStreamReloadReadsTheWholeLogWhenTheEndMissesTheWindow(t *testing.T) {
	h := newReloadTestHandle(t, 500)
	initial := h.wholeSnapshot(t)
	_, token := liveStructuredStreamUpdate("", h.projection(initial), false)
	reload := newStructuredStreamReload(h, false, initial)
	h.takeRequests()

	h.appendEntries(t, structuredStreamReloadSlack+1)
	snapshot, err := reload.read(context.Background(), token)
	if err != nil || snapshot == nil {
		t.Fatalf("read after a burst = %+v, %v; want a snapshot", snapshot, err)
	}
	got := h.takeRequests()
	if len(got) != 2 || got[0].Limit == 0 || got[1] != (worker.HistoryRequest{}) {
		t.Fatalf("history requests = %+v, want the end of the log and then the whole log", got)
	}
	update, _ := liveStructuredStreamUpdate(token, h.projection(snapshot), false)
	if update == nil || update.Operation != sessionStructuredOperationUpsert || len(update.StructuredMessages) != structuredStreamReloadSlack+2 {
		t.Fatalf("update = %+v, want an upsert from the cursor", update)
	}
}

func TestStructuredStreamReloadReadsTheWholeLogForAWholeToken(t *testing.T) {
	h := newReloadTestHandle(t, 5)
	initial := h.wholeSnapshot(t)
	wholeToken := buildStructuredStreamUpdate("", h.projection(initial), false).History.Cursor.ResumeToken
	reload := newStructuredStreamReload(h, false, initial)
	h.takeRequests()

	h.appendEntries(t, 1)
	if _, err := reload.read(context.Background(), wholeToken); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := h.takeRequests(); len(got) != 1 || got[0] != (worker.HistoryRequest{}) {
		t.Fatalf("history requests = %+v, want one whole read", got)
	}
}

func testStructuredMessages(from, to int) []SessionStructuredMessage {
	messages := make([]SessionStructuredMessage, 0, to-from)
	for i := from; i < to; i++ {
		messages = append(messages, testStructuredMessage(fmt.Sprintf("m%d", i), "final"))
	}
	return messages
}

// reloadTestHandle serves history from an in-memory entry list and keeps a
// real file in step with it, so the reload sees a new generation per write.
type reloadTestHandle struct {
	path string

	mu       sync.Mutex
	entries  int
	requests []worker.HistoryRequest
}

func newReloadTestHandle(t *testing.T, entries int) *reloadTestHandle {
	t.Helper()
	h := &reloadTestHandle{path: filepath.Join(t.TempDir(), "session.jsonl")}
	if err := os.WriteFile(h.path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.appendEntries(t, entries)
	return h
}

func (h *reloadTestHandle) appendEntries(t *testing.T, n int) {
	t.Helper()
	file, err := os.OpenFile(h.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close() //nolint:errcheck // test file
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := 0; i < n; i++ {
		if _, err := fmt.Fprintf(file, "{\"uuid\":\"m%d\"}\n", h.entries); err != nil {
			t.Fatal(err)
		}
		h.entries++
	}
}

func (h *reloadTestHandle) wholeSnapshot(t *testing.T) *worker.HistorySnapshot {
	t.Helper()
	snapshot, err := h.History(context.Background(), worker.HistoryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (h *reloadTestHandle) takeRequests() []worker.HistoryRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	requests := h.requests
	h.requests = nil
	return requests
}

func (h *reloadTestHandle) TranscriptPath(context.Context) (string, error) {
	return h.path, nil
}

func (h *reloadTestHandle) History(_ context.Context, req worker.HistoryRequest) (*worker.HistorySnapshot, error) {
	generation, err := worker.TranscriptGenerationID(h.path)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, req)
	from := 0
	var pagination *sessionlog.PaginationInfo
	if req.Limit > 0 && h.entries > req.Limit {
		from = h.entries - req.Limit
		pagination = &sessionlog.PaginationInfo{HasOlderMessages: true, PartialRead: true}
	}
	entries := make([]worker.HistoryEntry, 0, h.entries-from)
	for i := from; i < h.entries; i++ {
		id := fmt.Sprintf("m%d", i)
		entries = append(entries, worker.HistoryEntry{
			ID:     id,
			Kind:   "assistant",
			Actor:  worker.ActorAssistant,
			Status: worker.ResultStatusFinal,
			Text:   id + " text",
		})
	}
	return &worker.HistorySnapshot{
		GCSessionID:        "gc-1",
		ProviderSessionID:  "provider-1",
		TranscriptStreamID: filepath.Clean(h.path),
		Generation:         worker.Generation{ID: generation},
		Continuity:         worker.Continuity{Status: worker.ContinuityStatusContinuous},
		TailState:          worker.TailState{Activity: worker.TailActivityIdle},
		Pagination:         pagination,
		Entries:            entries,
	}, nil
}

// projection is what the structured streams build from a snapshot.
func (h *reloadTestHandle) projection(snapshot *worker.HistorySnapshot) SessionStreamStructuredMessageEvent {
	messages, _ := historySnapshotStructuredMessages(snapshot, false)
	return SessionStreamStructuredMessageEvent{
		ID:                 "gc-1",
		Provider:           "claude",
		Format:             "structured",
		SchemaVersion:      sessionStructuredSchemaVersion,
		History:            structuredHistoryFromSnapshot(snapshot),
		StructuredMessages: messages,
		Pagination:         snapshot.Pagination,
	}
}
