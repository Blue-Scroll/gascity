package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/sessionlog"
)

// A live conversation stream (the default format) opens on the newest
// compaction segment, like the raw stream, and not on the whole log
// (vn-6a437yc). Both routes serve it, so both are pinned.

type conversationStreamFixture struct {
	fs      *fakeState
	srv     *Server
	city    http.Handler
	info    session.Info
	logPath string
}

func newConversationStreamFixture(t *testing.T, lines ...string) conversationStreamFixture {
	t.Helper()
	fs := newSessionFakeState(t)
	searchBase := t.TempDir()
	srv := New(fs)
	city := newTestCityHandlerWith(t, fs, srv)
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
	writeNamedSessionJSONL(t, searchBase, workDir, info.SessionKey+".jsonl", lines...)
	return conversationStreamFixture{
		fs:      fs,
		srv:     srv,
		city:    city,
		info:    info,
		logPath: filepath.Join(searchBase, sessionlog.ProjectSlug(workDir), info.SessionKey+".jsonl"),
	}
}

// open starts a conversation stream on the legacy or the city route and
// returns its recorder, plus a stop that ends the stream and waits for it.
func (f conversationStreamFixture) open(t *testing.T, cityRoute bool) (*syncResponseRecorder, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	url := "/v0/session/" + f.info.ID + "/stream"
	var h http.Handler = f.srv
	if cityRoute {
		url = cityURL(f.fs, "/session/") + f.info.ID + "/stream"
		h = f.city
	}
	req := httptest.NewRequest("GET", url, nil).WithContext(ctx)
	rec := newSyncResponseRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()
	return rec, func() {
		cancel()
		<-done
	}
}

func (f conversationStreamFixture) appendLines(t *testing.T, lines ...string) {
	t.Helper()
	file, err := os.OpenFile(f.logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	_, err = file.WriteString(strings.Join(lines, "\n") + "\n")
	if closeErr := file.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("append transcript: %v", err)
	}
}

var conversationStreamRoutes = []struct {
	name string
	city bool
}{
	{name: "legacy"},
	{name: "huma-city", city: true},
}

func TestSessionStreamConversationOpensOnLatestCompactionTail(t *testing.T) {
	for _, route := range conversationStreamRoutes {
		t.Run(route.name, func(t *testing.T) {
			f := newConversationStreamFixture(t,
				`{"uuid":"a","parentUuid":"","type":"user","message":"{\"role\":\"user\",\"content\":\"before compaction\"}","timestamp":"2025-01-01T00:00:00Z"}`,
				`{"uuid":"cb0","parentUuid":"a","type":"system","subtype":"compact_boundary","timestamp":"2025-01-01T00:00:01Z"}`,
				`{"uuid":"b","parentUuid":"cb0","type":"assistant","message":"{\"role\":\"assistant\",\"content\":\"after first boundary\"}","timestamp":"2025-01-01T00:00:02Z"}`,
				`{"uuid":"cb1","parentUuid":"b","type":"system","subtype":"compact_boundary","timestamp":"2025-01-01T00:00:03Z"}`,
				`{"uuid":"c","parentUuid":"cb1","type":"assistant","message":"{\"role\":\"assistant\",\"content\":\"after second boundary\"}","timestamp":"2025-01-01T00:00:04Z"}`,
			)
			rec, stop := f.open(t, route.city)
			waitForRecorderSubstring(t, rec, "after second boundary", time.Second)

			// A turn that lands after the stream opened still arrives, and the
			// reload that carries it does not replay the older segments.
			f.appendLines(t,
				`{"uuid":"d","parentUuid":"c","type":"user","message":"{\"role\":\"user\",\"content\":\"new live turn\"}","timestamp":"2025-01-01T00:00:05Z"}`,
			)
			waitForRecorderSubstring(t, rec, "new live turn", 2*time.Second)
			stop()

			body := rec.BodyString()
			if !strings.Contains(body, "new live turn") {
				t.Fatalf("conversation stream missing the turn written after it opened: %s", body)
			}
			if strings.Contains(body, "before compaction") || strings.Contains(body, "after first boundary") {
				t.Fatalf("conversation stream sent older compaction segments: %s", body)
			}
		})
	}
}

func TestSessionStreamConversationEmptyTailDoesNotReplayLog(t *testing.T) {
	for _, route := range conversationStreamRoutes {
		t.Run(route.name, func(t *testing.T) {
			// The log ends on its second compaction boundary, so the newest
			// segment holds no turn when the stream opens. (A log with one
			// boundary is not sliced at all: sliceAtCompactBoundaries returns
			// the whole log when it has no more boundaries than asked for.)
			f := newConversationStreamFixture(t,
				`{"uuid":"a","parentUuid":"","type":"user","message":"{\"role\":\"user\",\"content\":\"before compaction\"}","timestamp":"2025-01-01T00:00:00Z"}`,
				`{"uuid":"cb0","parentUuid":"a","type":"system","subtype":"compact_boundary","timestamp":"2025-01-01T00:00:01Z"}`,
				`{"uuid":"b","parentUuid":"cb0","type":"assistant","message":"{\"role\":\"assistant\",\"content\":\"old answer\"}","timestamp":"2025-01-01T00:00:02Z"}`,
				`{"uuid":"cb1","parentUuid":"b","type":"system","subtype":"compact_boundary","timestamp":"2025-01-01T00:00:03Z"}`,
			)
			rec, stop := f.open(t, route.city)
			// Let the stream send its opening frames before the log grows.
			time.Sleep(200 * time.Millisecond)

			f.appendLines(t,
				`{"uuid":"c","parentUuid":"cb1","type":"user","message":"{\"role\":\"user\",\"content\":\"first turn after compaction\"}","timestamp":"2025-01-01T00:00:04Z"}`,
			)
			waitForRecorderSubstring(t, rec, "first turn after compaction", 2*time.Second)
			stop()

			body := rec.BodyString()
			if !strings.Contains(body, "first turn after compaction") {
				t.Fatalf("conversation stream missing the first turn after compaction: %s", body)
			}
			if strings.Contains(body, "before compaction") || strings.Contains(body, "old answer") {
				t.Fatalf("conversation stream replayed the log before the newest boundary: %s", body)
			}
		})
	}
}
