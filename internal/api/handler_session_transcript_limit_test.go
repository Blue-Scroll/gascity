package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/sessionlog"
	"github.com/gastownhall/gascity/internal/worker"
)

type limitTranscriptFixture struct {
	fs      *fakeState
	srv     *Server
	h       http.Handler
	info    session.Info
	logPath string
}

func newLimitTranscriptFixture(t *testing.T, entries int) limitTranscriptFixture {
	t.Helper()
	fs := newSessionFakeState(t)
	searchBase := t.TempDir()
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	srv.sessionLogSearchPaths = []string{searchBase}

	mgr := session.NewManagerWithOptions(fs.cityBeadStore, fs.sp)
	resume := session.ProviderResume{
		ResumeFlag:    "--resume",
		ResumeStyle:   "flag",
		SessionIDFlag: "--session-id",
	}
	workDir := t.TempDir()
	info, err := mgr.CreateSession(context.Background(), session.CreateOptions{Template: "myrig/worker", Title: "Chat", Command: "claude", WorkDir: workDir, Provider: "claude", Env: nil, Resume: resume, Hints: runtime.Config{}, ExtraMeta: map[string]string{"session_origin": "manual"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	lines := make([]string, 0, entries)
	for i := 1; i <= entries; i++ {
		lines = append(lines, limitTranscriptLine(i))
	}
	writeNamedSessionJSONL(t, searchBase, workDir, info.SessionKey+".jsonl", lines...)
	return limitTranscriptFixture{
		fs:      fs,
		srv:     srv,
		h:       h,
		info:    info,
		logPath: filepath.Join(searchBase, sessionlog.ProjectSlug(workDir), info.SessionKey+".jsonl"),
	}
}

func limitTranscriptLine(i int) string {
	role := "user"
	if i%2 == 0 {
		role = "assistant"
	}
	parent := ""
	if i > 1 {
		parent = fmt.Sprint(i - 1)
	}
	return fmt.Sprintf(`{"uuid":"%d","parentUuid":"%s","type":"%s","message":"{\"role\":\"%s\",\"content\":\"entry %d\"}","timestamp":"2025-01-01T00:00:%02dZ"}`,
		i, parent, role, role, i, i)
}

type limitTranscriptResponse struct {
	Turns              []json.RawMessage `json:"turns"`
	Messages           []json.RawMessage `json:"messages"`
	StructuredMessages []struct {
		ID string `json:"id"`
	} `json:"structured_messages"`
	History *struct {
		Cursor struct {
			ResumeToken string `json:"resume_token"`
		} `json:"cursor"`
	} `json:"history"`
	Pagination *sessionlog.PaginationInfo `json:"pagination"`
}

func (f limitTranscriptFixture) get(t *testing.T, handler http.Handler, path string) limitTranscriptResponse {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200; body: %s", path, w.Code, w.Body.String())
	}
	var response limitTranscriptResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode transcript response: %v", err)
	}
	if response.Pagination == nil {
		t.Fatalf("GET %s: pagination is nil", path)
	}
	return response
}

func (r limitTranscriptResponse) count(format string) int {
	switch format {
	case "raw":
		return len(r.Messages)
	case "structured":
		return len(r.StructuredMessages)
	default:
		return len(r.Turns)
	}
}

// A limit is the smallest window a reader can ask for. Without it the
// smallest was one whole compaction segment: 1842 messages for the mayor on
// 2026-10-14, when the phone view draws 60 (vn-c5p1mf0).
func TestHandleSessionTranscriptLimitReturnsNewestEntriesOnEverySurface(t *testing.T) {
	f := newLimitTranscriptFixture(t, 5)
	surfaces := []struct {
		name    string
		path    string
		handler http.Handler
	}{
		{name: "city-huma", path: cityURL(f.fs, "/session/") + f.info.ID + "/transcript", handler: f.h},
		{name: "legacy", path: "/v0/session/" + f.info.ID + "/transcript", handler: f.srv.legacySessionHandler()},
	}
	reads := []struct {
		name      string
		query     string
		wantOlder bool
		wantNewer bool
	}{
		{name: "newest", query: "limit=2", wantOlder: true},
		{name: "newest-with-tail", query: "tail=1&limit=2", wantOlder: true},
		{name: "older-page", query: "before=4&limit=2", wantOlder: true, wantNewer: true},
		{name: "newer-page", query: "after=1&limit=2", wantOlder: true, wantNewer: true},
	}

	for _, surface := range surfaces {
		for _, format := range []string{"conversation", "raw", "structured"} {
			for _, read := range reads {
				t.Run(surface.name+"/"+format+"/"+read.name, func(t *testing.T) {
					response := f.get(t, surface.handler, surface.path+"?format="+format+"&"+read.query)
					if got := response.count(format); got != 2 {
						t.Fatalf("returned %d %s items, want 2", got, format)
					}
					p := response.Pagination
					if p.TotalMessageCount != 5 || p.ReturnedMessageCount != 2 {
						t.Fatalf("pagination = %+v, want total=5 returned=2", *p)
					}
					if p.HasOlderMessages != read.wantOlder || p.HasNewerMessages != read.wantNewer {
						t.Fatalf("pagination flags = older:%t newer:%t, want older:%t newer:%t", p.HasOlderMessages, p.HasNewerMessages, read.wantOlder, read.wantNewer)
					}
					if format == "structured" {
						got := []string{response.StructuredMessages[0].ID, response.StructuredMessages[1].ID}
						want := map[string][]string{
							"newest":           {"4", "5"},
							"newest-with-tail": {"4", "5"},
							"older-page":       {"2", "3"},
							"newer-page":       {"2", "3"},
						}[read.name]
						if !equalStrings(got, want) {
							t.Fatalf("structured IDs = %v, want %v", got, want)
						}
					}
				})
			}
		}
	}
}

func TestHandleSessionTranscriptLimitLargerThanLogReturnsAll(t *testing.T) {
	f := newLimitTranscriptFixture(t, 3)
	response := f.get(t, f.h, cityURL(f.fs, "/session/")+f.info.ID+"/transcript?format=structured&limit=60")
	if len(response.StructuredMessages) != 3 || response.Pagination.HasOlderMessages {
		t.Fatalf("messages=%d pagination=%+v, want all 3 and no older", len(response.StructuredMessages), *response.Pagination)
	}
}

func TestHandleSessionTranscriptLimitRejectsNegative(t *testing.T) {
	f := newLimitTranscriptFixture(t, 1)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, cityURL(f.fs, "/session/")+f.info.ID+"/transcript?limit=-1", nil))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422; body: %s", w.Code, w.Body.String())
	}
}

// The dashboard takes a limited REST snapshot, then opens the stream with its
// resume token. The stream projects the whole log, so the token must be a
// suffix window: the stream answers with an upsert from the window's last
// message, never a reset that throws the page away.
func TestHandleSessionTranscriptLimitTokenResumesStreamWithUpsert(t *testing.T) {
	f := newLimitTranscriptFixture(t, 5)
	snapshot := f.get(t, f.h, cityURL(f.fs, "/session/")+f.info.ID+"/transcript?format=structured&limit=2")
	if snapshot.History == nil || snapshot.History.Cursor.ResumeToken == "" {
		t.Fatal("limited snapshot has no resume token")
	}
	token, ok := decodeStructuredResumeToken(snapshot.History.Cursor.ResumeToken)
	if !ok || !token.SuffixWindow {
		t.Fatalf("resume token = %+v (ok=%t), want a suffix window", token, ok)
	}

	logFile, err := os.OpenFile(f.logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.WriteString(limitTranscriptLine(6) + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	// Build the projection the way the live stream does: the whole history.
	handle, err := f.srv.workerHandleForSession(f.fs.cityBeadStore, f.info.ID)
	if err != nil {
		t.Fatalf("worker handle: %v", err)
	}
	history, err := handle.History(worker.WithoutOperationEvents(context.Background()), worker.HistoryRequest{})
	if err != nil {
		t.Fatalf("full history: %v", err)
	}
	messages, _ := historySnapshotStructuredMessages(history, false)
	projection := SessionStreamStructuredMessageEvent{
		ID:                 f.info.ID,
		Template:           f.info.Template,
		Provider:           f.info.Provider,
		Format:             "structured",
		SchemaVersion:      sessionStructuredSchemaVersion,
		History:            structuredHistoryFromSnapshot(history),
		StructuredMessages: messages,
		Pagination:         history.Pagination,
	}

	update := buildStructuredStreamUpdate(snapshot.History.Cursor.ResumeToken, projection, false)
	if update == nil {
		t.Fatal("update = nil, want an upsert carrying the new message")
	}
	if update.Operation != sessionStructuredOperationUpsert {
		t.Fatalf("operation = %q (reset_reason %q), want upsert", update.Operation, update.ResetReason)
	}
	if got := structuredMessageIDs(update.StructuredMessages); !equalStrings(got, []string{"5", "6"}) {
		t.Fatalf("upsert IDs = %v, want [5 6]", got)
	}
}
