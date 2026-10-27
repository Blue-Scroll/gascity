package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/testutil"
)

// pendingTransitions returns the session.pending / session.pending_cleared
// events recorded so far, in seq order.
func pendingTransitions(t *testing.T, ep events.Provider) []events.Event {
	t.Helper()
	all, err := ep.List(events.Filter{})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []events.Event
	for _, e := range all {
		if e.Type == events.SessionPending || e.Type == events.SessionPendingCleared {
			out = append(out, e)
		}
	}
	return out
}

func decodePendingPayload(t *testing.T, e events.Event) SessionPendingPayload {
	t.Helper()
	if e.Type != events.SessionPending {
		t.Fatalf("event type = %q, want %q", e.Type, events.SessionPending)
	}
	var p SessionPendingPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("decode %s payload: %v", e.Type, err)
	}
	return p
}

func decodeClearedPayload(t *testing.T, e events.Event) SessionPendingClearedPayload {
	t.Helper()
	if e.Type != events.SessionPendingCleared {
		t.Fatalf("event type = %q, want %q", e.Type, events.SessionPendingCleared)
	}
	var p SessionPendingClearedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("decode %s payload: %v", e.Type, err)
	}
	return p
}

func approvalFor(requestID string) *runtime.PendingInteraction {
	return &runtime.PendingInteraction{
		RequestID: requestID,
		Kind:      "approval",
		Prompt:    "Bash: rm -rf build",
		Options:   []string{"Yes", "Yes, and don't ask again for rm commands", "No"},
		Metadata:  map[string]string{"tool_name": "Bash", "source": "tmux"},
	}
}

func TestPendingEventTypesAreRegistered(t *testing.T) {
	for _, typ := range []string{events.SessionPending, events.SessionPendingCleared} {
		found := false
		for _, known := range events.KnownEventTypes {
			if known == typ {
				found = true
			}
		}
		if !found {
			t.Errorf("%q missing from events.KnownEventTypes", typ)
		}
		if _, ok := events.LookupPayload(typ); !ok {
			t.Errorf("%q has no registered payload", typ)
		}
	}
}

// A pending interaction is announced once, with everything a client needs to
// answer it, no matter how many detection polls see it.
func TestPendingMonitorEmitsPendingOnceAcrossPolls(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	waiting := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	_ = createTestSession(t, fs.cityBeadStore, fs.sp, "Idle")
	fs.sp.SetPendingInteraction(waiting.SessionName, approvalFor("req-1"))

	m := newPendingMonitor(fs.cityPath)
	for i := 0; i < 3; i++ {
		m.cycle(srv)
	}

	got := pendingTransitions(t, fs.eventProv)
	if len(got) != 1 {
		t.Fatalf("got %d pending transitions after 3 polls, want exactly 1: %#v", len(got), got)
	}
	e := got[0]
	if e.Subject != waiting.ID || e.SessionID != waiting.ID {
		t.Fatalf("envelope subject=%q session_id=%q, want both %q", e.Subject, e.SessionID, waiting.ID)
	}
	p := decodePendingPayload(t, e)
	want := approvalFor("req-1")
	if p.SessionID != waiting.ID || p.RequestID != want.RequestID || p.Kind != want.Kind ||
		p.Prompt != want.Prompt || strings.Join(p.Options, "|") != strings.Join(want.Options, "|") ||
		p.Metadata["tool_name"] != "Bash" || p.Template != waiting.Template {
		t.Fatalf("payload = %#v, want the full interaction for session %s", p, waiting.ID)
	}
}

// Answering the interaction clears it exactly once, with the same ids.
func TestPendingMonitorEmitsClearedOnceAfterRespond(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-1"))

	m := newPendingMonitor(fs.cityPath)
	m.cycle(srv)

	req := newPostRequest(cityURL(fs, "/session/")+info.ID+"/respond", strings.NewReader(`{"action":"approve","request_id":"req-1"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("respond status = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}

	m.cycle(srv)
	m.cycle(srv)

	got := pendingTransitions(t, fs.eventProv)
	if len(got) != 2 {
		t.Fatalf("got %d transitions, want pending then cleared: %#v", len(got), got)
	}
	cleared := decodeClearedPayload(t, got[1])
	if cleared.SessionID != info.ID || cleared.RequestID != "req-1" || cleared.Kind != "approval" || cleared.Reason != PendingClearedResolved {
		t.Fatalf("cleared payload = %#v, want session %s req-1 approval resolved", cleared, info.ID)
	}
	if got[1].Subject != info.ID || got[1].SessionID != info.ID {
		t.Fatalf("cleared envelope subject=%q session_id=%q, want %q", got[1].Subject, got[1].SessionID, info.ID)
	}
}

// A different interaction on the same session is a clear of the old one
// followed by a pending for the new one.
func TestPendingMonitorReplacedInteraction(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-1"))

	m := newPendingMonitor(fs.cityPath)
	m.cycle(srv)
	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-2"))
	m.cycle(srv)
	m.cycle(srv)

	got := pendingTransitions(t, fs.eventProv)
	if len(got) != 3 {
		t.Fatalf("got %d transitions, want pending/cleared/pending: %#v", len(got), got)
	}
	if p := decodePendingPayload(t, got[0]); p.RequestID != "req-1" {
		t.Fatalf("first pending = %#v, want req-1", p)
	}
	if c := decodeClearedPayload(t, got[1]); c.RequestID != "req-1" || c.Reason != PendingClearedReplaced {
		t.Fatalf("cleared = %#v, want req-1 replaced", c)
	}
	if p := decodePendingPayload(t, got[2]); p.RequestID != "req-2" {
		t.Fatalf("second pending = %#v, want req-2", p)
	}
}

// A session that leaves the probed set clears its interaction.
func TestPendingMonitorClearsWhenSessionGone(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-1"))

	m := newPendingMonitor(fs.cityPath)
	m.cycle(srv)
	suspendSessionForPermissionModeTest(t, fs, info.ID)
	m.cycle(srv)
	m.cycle(srv)

	got := pendingTransitions(t, fs.eventProv)
	if len(got) != 2 {
		t.Fatalf("got %d transitions, want pending then cleared: %#v", len(got), got)
	}
	if c := decodeClearedPayload(t, got[1]); c.SessionID != info.ID || c.RequestID != "req-1" || c.Reason != PendingClearedSessionGone {
		t.Fatalf("cleared = %#v, want %s req-1 session_gone", c, info.ID)
	}
}

// A failed probe says nothing about the interaction, so it must not clear it.
func TestPendingMonitorProbeErrorDoesNotClear(t *testing.T) {
	fs := newSessionFakeState(t)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-1"))
	failing := &pendingPerSessionErrorProvider{Fake: fs.sp, failErr: errors.New("capturing pane: probe blew up")}
	state := &stateWithSessionProvider{fakeState: fs, provider: failing}
	srv := New(state)

	m := newPendingMonitor(fs.cityPath)
	m.cycle(srv)
	failing.failName = info.SessionName
	m.cycle(srv)
	failing.failName = ""
	m.cycle(srv)

	got := pendingTransitions(t, fs.eventProv)
	if len(got) != 1 || got[0].Type != events.SessionPending {
		t.Fatalf("transitions = %#v, want only the original session.pending", got)
	}
}

// A restarted monitor (new process) picks up what was already published, so
// it emits only the transitions that happened while it was down.
func TestPendingMonitorRestartEmitsOnlyMissedTransitions(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	answered := createTestSession(t, fs.cityBeadStore, fs.sp, "Answered")
	still := createTestSession(t, fs.cityBeadStore, fs.sp, "Still")
	fs.sp.SetPendingInteraction(answered.SessionName, approvalFor("req-a"))
	fs.sp.SetPendingInteraction(still.SessionName, approvalFor("req-b"))

	newPendingMonitor(fs.cityPath).cycle(srv)
	if got := pendingTransitions(t, fs.eventProv); len(got) != 2 {
		t.Fatalf("before restart: %d transitions, want 2 pending: %#v", len(got), got)
	}

	// Down: the first interaction is answered at the terminal.
	fs.sp.SetPendingInteraction(answered.SessionName, nil)

	restarted := newPendingMonitor(fs.cityPath)
	restarted.cycle(New(fs))
	restarted.cycle(New(fs))

	got := pendingTransitions(t, fs.eventProv)
	if len(got) != 3 {
		t.Fatalf("after restart: %d transitions, want 2 pending + 1 cleared: %#v", len(got), got)
	}
	if c := decodeClearedPayload(t, got[2]); c.SessionID != answered.ID || c.RequestID != "req-a" || c.Reason != PendingClearedResolved {
		t.Fatalf("cleared after restart = %#v, want %s req-a resolved", c, answered.ID)
	}
}

// failingAckProvider drops the first n RecordAck calls.
type failingAckProvider struct {
	*events.Fake
	drops int
}

func (p *failingAckProvider) RecordAck(e events.Event) error {
	if p.drops > 0 {
		p.drops--
		return errors.New("events: append dropped")
	}
	return p.Fake.RecordAck(e)
}

// A transition whose event was dropped is retried on the next poll instead of
// being marked published.
func TestPendingMonitorRetriesDroppedEvent(t *testing.T) {
	fs := newSessionFakeState(t)
	ep := &failingAckProvider{Fake: events.NewFake(), drops: 1}
	fs.eventProv = ep
	srv := New(fs)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-1"))

	m := newPendingMonitor(fs.cityPath)
	m.cycle(srv)
	if got := pendingTransitions(t, ep); len(got) != 0 {
		t.Fatalf("dropped append still produced %d events", len(got))
	}
	m.cycle(srv)
	m.cycle(srv)
	if got := pendingTransitions(t, ep); len(got) != 1 {
		t.Fatalf("got %d transitions after retry, want exactly 1: %#v", len(got), got)
	}
}

// openCityEventStream serves the city event stream in-process.
func openCityEventStream(t *testing.T, h http.Handler, state State, lastEventID string) *sseStream {
	t.Helper()
	return openSSEStream(t, h, cityURL(state, "/events/stream"), lastEventID)
}

// waitForFrame returns the first frame whose JSON type is eventType.
func waitForFrame(t *testing.T, frames <-chan sseTestFrame, eventType string) sseTestFrame {
	t.Helper()
	deadline := time.After(testutil.GoroutineRaceTimeout)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("stream closed before a %s frame", eventType)
			}
			var env struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(f.Data), &env); err != nil {
				continue
			}
			if env.Type == eventType {
				return f
			}
		case <-deadline:
			t.Fatalf("no %s frame within %s", eventType, testutil.GoroutineRaceTimeout)
		}
	}
}

func fastPendingMonitor(t *testing.T) {
	t.Helper()
	prev := pendingMonitorInterval
	pendingMonitorInterval = 20 * time.Millisecond
	t.Cleanup(func() { pendingMonitorInterval = prev })
}

// assertPendingMonitorStopped fails unless the city's monitor has no lease
// and no detection loop: with no loop, nothing can probe or publish.
func assertPendingMonitorStopped(t *testing.T, cityPath string) {
	t.Helper()
	m := pendingMonitorFor(cityPath)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refs != 0 || m.done != nil {
		t.Fatalf("pending monitor for %s still running: refs=%d loop=%v", cityPath, m.refs, m.done != nil)
	}
}

// The city event stream carries the pending and cleared transitions to a
// client that only watches the stream.
func TestCityEventStreamCarriesPendingTransitions(t *testing.T) {
	fastPendingMonitor(t)
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")

	stream := openCityEventStream(t, h, fs, "")

	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-1"))
	pending := waitForFrame(t, stream.frames, events.SessionPending)
	if !strings.Contains(pending.Data, `"request_id":"req-1"`) || !strings.Contains(pending.Data, `"prompt":"Bash: rm -rf build"`) {
		t.Fatalf("session.pending frame lacks the interaction: %s", pending.Data)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newPostRequest(cityURL(fs, "/session/")+info.ID+"/respond", strings.NewReader(`{"action":"approve","request_id":"req-1"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("respond status = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	cleared := waitForFrame(t, stream.frames, events.SessionPendingCleared)
	if !strings.Contains(cleared.Data, `"request_id":"req-1"`) || !strings.Contains(cleared.Data, `"reason":"resolved"`) {
		t.Fatalf("session.pending_cleared frame = %s", cleared.Data)
	}

	// Many more detection passes over the same state: still exactly one of
	// each. The passes run on the stream's own monitor, interleaved with its
	// loop.
	m := pendingMonitorFor(fs.cityPath)
	for i := 0; i < 10; i++ {
		m.cycle(srv)
	}
	if got := pendingTransitions(t, fs.eventProv); len(got) != 2 {
		t.Fatalf("got %d transitions, want exactly 2: %#v", len(got), got)
	}
}

// Detection costs nothing while no event stream is open for the city: the
// monitor's loop exists only while a stream holds a lease.
func TestPendingMonitorProbesOnlyWhileStreamOpen(t *testing.T) {
	fastPendingMonitor(t)
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-1"))

	assertPendingMonitorStopped(t, fs.cityPath)
	if n := fs.sp.CountCalls("Pending", info.SessionName); n != 0 {
		t.Fatalf("probed %d times with no stream open, want 0", n)
	}

	stream := openCityEventStream(t, h, fs, "")
	waitForFrame(t, stream.frames, events.SessionPending)
	if n := fs.sp.CountCalls("Pending", info.SessionName); n == 0 {
		t.Fatal("no probes while the stream was open")
	}
	stream.stop()

	assertPendingMonitorStopped(t, fs.cityPath)
}

// A client that resumes with Last-Event-ID receives the clear for an
// interaction answered while it (and so the monitor) was away.
func TestEventStreamResumeDeliversClearMissedWhileDisconnected(t *testing.T) {
	fastPendingMonitor(t)
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-1"))

	stream := openCityEventStream(t, h, fs, "")
	pending := waitForFrame(t, stream.frames, events.SessionPending)
	stream.stop()
	assertPendingMonitorStopped(t, fs.cityPath)

	// Answered at the terminal while nobody watches. With the monitor stopped
	// nothing observes it until a client returns.
	fs.sp.SetPendingInteraction(info.SessionName, nil)
	if got := pendingTransitions(t, fs.eventProv); len(got) != 1 {
		t.Fatalf("got %d transitions while disconnected, want only the pending: %#v", len(got), got)
	}

	resumed := openCityEventStream(t, h, fs, pending.ID)
	cleared := waitForFrame(t, resumed.frames, events.SessionPendingCleared)
	if !strings.Contains(cleared.Data, `"request_id":"req-1"`) {
		t.Fatalf("resumed clear = %s, want req-1", cleared.Data)
	}
}

// The supervisor-scope stream carries pending transitions for every city it
// watches, including a city that starts after the client connected.
func TestSupervisorEventStreamCarriesPendingTransitions(t *testing.T) {
	fastPendingMonitor(t)
	alpha := newSessionFakeState(t)
	alpha.cityName = "alpha"
	alphaInfo := createTestSession(t, alpha.cityBeadStore, alpha.sp, "Waiting")
	alpha.sp.SetPendingInteraction(alphaInfo.SessionName, approvalFor("req-alpha"))
	resolver := newDynamicCityResolver(map[string]*fakeState{"alpha": alpha})
	sm := NewSupervisorMux(resolver, nil, false, "test", "", time.Now())
	stream := openLiveGlobalStream(t, sm, "", "")

	frame, data := stream.nextTaggedEvent(t)
	if data["type"] != events.SessionPending || data["city"] != "alpha" || data["subject"] != alphaInfo.ID {
		t.Fatalf("first tagged_event = %s, want alpha session.pending for %s", frame.Data, alphaInfo.ID)
	}

	beta := newSessionFakeState(t)
	beta.cityName = "beta"
	betaInfo := createTestSession(t, beta.cityBeadStore, beta.sp, "Waiting")
	beta.sp.SetPendingInteraction(betaInfo.SessionName, approvalFor("req-beta"))
	resolver.addCity(beta)

	frame, data = stream.nextTaggedEvent(t)
	if data["type"] != events.SessionPending || data["city"] != "beta" || data["subject"] != betaInfo.ID {
		t.Fatalf("second tagged_event = %s, want beta session.pending for %s", frame.Data, betaInfo.ID)
	}
}

// slowPendingMonitor makes a started monitor run one cycle and then rest for
// the rest of the test, so every later probe is one a GET made.
func slowPendingMonitor(t *testing.T) {
	t.Helper()
	prev := pendingMonitorInterval
	pendingMonitorInterval = time.Hour
	t.Cleanup(func() { pendingMonitorInterval = prev })
}

// startPendingMonitor holds a lease on the city's monitor, as an open event
// stream does, and waits for its first snapshot.
func startPendingMonitor(t *testing.T, srv *Server, cityPath string) func() {
	t.Helper()
	release := srv.acquirePendingMonitor()
	t.Cleanup(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := pendingMonitorFor(cityPath).freshSnapshot(); ok {
			return release
		}
		if time.Now().After(deadline) {
			t.Fatal("pending monitor stored no snapshot within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func getSessionPending(t *testing.T, h http.Handler, url string) sessionPendingResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d; body: %s", url, rec.Code, rec.Body.String())
	}
	var resp sessionPendingResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return resp
}

// While the monitor runs, both pending GETs answer from its snapshot and run
// no probe of their own. A dashboard polling every session's pending used to
// run one tmux capture per session per poll, 2 to 8 s each (vn-8ckrsgv).
func TestPendingGetsShareRunningMonitorSnapshot(t *testing.T) {
	slowPendingMonitor(t)
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	waiting := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	idle := createTestSession(t, fs.cityBeadStore, fs.sp, "Idle")
	fs.sp.SetPendingInteraction(waiting.SessionName, approvalFor("req-1"))

	startPendingMonitor(t, srv, fs.cityPath)
	before := fs.sp.CountCalls("Pending", waiting.SessionName) + fs.sp.CountCalls("Pending", idle.SessionName)

	for i := 0; i < 3; i++ {
		got := getSessionPending(t, h, cityURL(fs, "/session/")+waiting.ID+"/pending")
		if !got.Supported || got.Pending == nil || got.Pending.RequestID != "req-1" || got.Pending.Prompt != approvalFor("req-1").Prompt {
			t.Fatalf("waiting session pending = %#v, want the full req-1 interaction", got)
		}
		if got := getSessionPending(t, h, cityURL(fs, "/session/")+idle.ID+"/pending"); !got.Supported || got.Pending != nil {
			t.Fatalf("idle session pending = %#v, want supported and none", got)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", cityURL(fs, "/pending"), nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"req-1"`) {
			t.Fatalf("city pending = %d %s, want req-1 listed", rec.Code, rec.Body.String())
		}
	}

	after := fs.sp.CountCalls("Pending", waiting.SessionName) + fs.sp.CountCalls("Pending", idle.SessionName)
	if after != before {
		t.Fatalf("GETs ran %d probes of their own while the monitor ran, want 0", after-before)
	}
}

// An answer drops the snapshot, so the next GET probes live and does not show
// the interaction that was just answered.
func TestPendingGetProbesLiveAfterAnswer(t *testing.T) {
	slowPendingMonitor(t)
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	fs.sp.SetPendingInteraction(info.SessionName, approvalFor("req-1"))
	startPendingMonitor(t, srv, fs.cityPath)

	fs.sp.SetPendingInteraction(info.SessionName, nil)
	srv.pokePendingMonitor()

	if got := getSessionPending(t, h, cityURL(fs, "/session/")+info.ID+"/pending"); got.Pending != nil {
		t.Fatalf("pending after the answer = %#v, want none", got.Pending)
	}
}

// A cycle whose probe began before an answer must not store what it saw: the
// snapshot would show the answered interaction until the next cycle.
func TestPendingSnapshotFromBeforeADropIsNotStored(t *testing.T) {
	m := newPendingMonitor(t.TempDir())
	m.done = make(chan struct{}) // running, as far as freshSnapshot can tell

	gen := m.snapshotGen()
	m.dropSnapshot()
	m.storeSnapshot(gen, cityPendingSnapshot{})
	if _, ok := m.freshSnapshot(); ok {
		t.Fatal("a snapshot probed before the drop was served")
	}

	m.storeSnapshot(m.snapshotGen(), cityPendingSnapshot{})
	if _, ok := m.freshSnapshot(); !ok {
		t.Fatal("a snapshot probed after the drop was not served")
	}
}

// A snapshot is served only while the loop that refreshes it runs, and only
// while it is younger than pendingSnapshotMaxAge.
func TestPendingSnapshotServedOnlyWhileFresh(t *testing.T) {
	slowPendingMonitor(t)
	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Waiting")
	url := cityURL(fs, "/session/") + info.ID + "/pending"

	release := startPendingMonitor(t, srv, fs.cityPath)
	m := pendingMonitorFor(fs.cityPath)
	m.snapMu.Lock()
	m.snapAt = time.Now().Add(-2 * pendingSnapshotMaxAge)
	m.snapMu.Unlock()
	before := fs.sp.CountCalls("Pending", info.SessionName)
	getSessionPending(t, h, url)
	if fs.sp.CountCalls("Pending", info.SessionName) == before {
		t.Fatal("a GET answered from a snapshot older than pendingSnapshotMaxAge")
	}

	release()
	assertPendingMonitorStopped(t, fs.cityPath)
	before = fs.sp.CountCalls("Pending", info.SessionName)
	getSessionPending(t, h, url)
	if fs.sp.CountCalls("Pending", info.SessionName) == before {
		t.Fatal("a GET answered from a stopped monitor's snapshot")
	}
}

// After a slow cycle the monitor rests as long as the cycle took, so on a
// loaded host it probes at most half the time instead of back to back.
func TestPendingMonitorRestsAsLongAsASlowCycle(t *testing.T) {
	prev := pendingMonitorInterval
	pendingMonitorInterval = 2 * time.Second
	t.Cleanup(func() { pendingMonitorInterval = prev })

	if got := pendingMonitorRest(100 * time.Millisecond); got != 2*time.Second {
		t.Errorf("rest after a 100ms cycle = %s, want the 2s interval", got)
	}
	if got := pendingMonitorRest(16 * time.Second); got != 16*time.Second {
		t.Errorf("rest after a 16s cycle = %s, want 16s", got)
	}
}
