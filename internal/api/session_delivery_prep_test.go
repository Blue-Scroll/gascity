package api

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// untouchableStore panics on any call: its embedded Store is nil. A function
// that is handed one and returns cleanly read nothing from it.
type untouchableStore struct{ beads.Store }

// The async message and submit paths reuse the target the 202 gate resolved.
// A second resolve repeated every store read before delivery (vn-lwg9dmi).
func TestResolveDeliverableSessionReusesGateTarget(t *testing.T) {
	srv := New(newSessionFakeState(t))

	target, err := srv.resolveDeliverableSession(context.Background(), untouchableStore{}, "mayor", resolvedSessionTarget{id: "gc-42"})
	if err != nil {
		t.Fatalf("resolveDeliverableSession: %v", err)
	}
	if target.id != "gc-42" {
		t.Fatalf("id = %q, want the gate's id gc-42", target.id)
	}
}

// The gate returns a zero target for a configured named session with no bead
// yet, so the async path must still materialize it.
func TestSessionTargetDeliverableLeavesNamedSessionToMaterialize(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)

	gate, err := srv.sessionTargetDeliverable(context.Background(), fs.cityBeadStore, "worker")
	if err != nil {
		t.Fatalf("sessionTargetDeliverable: %v", err)
	}
	if gate.id != "" || gate.record != nil {
		t.Fatalf("gate = %+v, want a zero target for a named session with no bead", gate)
	}

	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Live")
	gate, err = srv.sessionTargetDeliverable(context.Background(), fs.cityBeadStore, info.ID)
	if err != nil {
		t.Fatalf("sessionTargetDeliverable(%s): %v", info.ID, err)
	}
	if gate.id != info.ID {
		t.Fatalf("gate id = %q, want %q", gate.id, info.ID)
	}
}

// A send addressed by session id reads the session once, in the 202 gate. The
// handle is built from that record: the second store read was most of the
// handle phase, the slowest prep phase in 306 of 313 sends over 300ms
// (vn-q4axjq5).
func TestDeliverableSessionHandleReusesGateRecord(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Once")
	store := &getCountingStore{Store: fs.cityBeadStore}

	gate, err := srv.sessionTargetDeliverable(context.Background(), store, info.ID)
	if err != nil {
		t.Fatalf("sessionTargetDeliverable: %v", err)
	}
	if gate.record == nil {
		t.Fatalf("gate record = nil, want the record its exact-ID read returned")
	}
	if got := store.gets.Load(); got != 1 {
		t.Fatalf("gate read the session %d times, want 1", got)
	}

	target, err := srv.resolveDeliverableSession(context.Background(), store, info.ID, gate)
	if err != nil {
		t.Fatalf("resolveDeliverableSession: %v", err)
	}
	var prep sessionDeliveryPrep
	prep.resolved = time.Now()
	handle, err := srv.deliverableSessionHandle(store, target, &prep)
	if err != nil {
		t.Fatalf("deliverableSessionHandle: %v", err)
	}
	if handle == nil {
		t.Fatalf("handle = nil")
	}
	if got := store.gets.Load(); got != 1 {
		t.Fatalf("send read the session %d times, want 1 (the gate's read only)", got)
	}
	if prep.got.IsZero() || prep.built.Before(prep.got) {
		t.Fatalf("prep = %+v, want got and built stamped in order", prep)
	}
}

// A target the gate could not read (a named session it left to materialize)
// still gets one record read, and a handle.
func TestDeliverableSessionHandleReadsRecordWhenGateHadNone(t *testing.T) {
	fs := newSessionFakeState(t)
	srv := New(fs)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Late")
	store := &getCountingStore{Store: fs.cityBeadStore}

	var prep sessionDeliveryPrep
	handle, err := srv.deliverableSessionHandle(store, resolvedSessionTarget{id: info.ID}, &prep)
	if err != nil {
		t.Fatalf("deliverableSessionHandle: %v", err)
	}
	if handle == nil {
		t.Fatalf("handle = nil")
	}
	if got := store.gets.Load(); got != 1 {
		t.Fatalf("read the session %d times, want 1", got)
	}
}

// syncBuffer lets the test read the log while the async goroutine writes it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Each async send logs its prep phases by name, so a slow phase at high load
// can be found in the supervisor log instead of guessed (vn-lwg9dmi).
func TestSessionMessageAndSubmitLogDeliveryPrep(t *testing.T) {
	logs := &syncBuffer{}
	oldOutput := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOutput)
		log.SetFlags(oldFlags)
	})

	fs := newSessionFakeState(t)
	srv := New(fs)
	h := newTestCityHandlerWith(t, fs, srv)
	info := createTestSession(t, fs.cityBeadStore, fs.sp, "Prep")

	for _, tc := range []struct {
		op   string
		path string
		wait func(requestID string)
	}{
		{
			op:   RequestOperationSessionMessage,
			path: "/messages",
			wait: func(requestID string) {
				if success, failure := waitForSessionMessageResult(t, fs.eventProv, requestID); success == nil {
					t.Fatalf("session message failed: %s: %s", failure.ErrorCode, failure.ErrorMessage)
				}
			},
		},
		{
			op:   RequestOperationSessionSubmit,
			path: "/submit",
			wait: func(requestID string) {
				if success, failure := waitForSessionSubmitResult(t, fs.eventProv, requestID); success == nil {
					t.Fatalf("session submit failed: %s: %s", failure.ErrorCode, failure.ErrorMessage)
				}
			},
		},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newPostRequest(cityURL(fs, "/session/")+info.ID+tc.path, strings.NewReader(`{"message":"hello"}`)))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d, want 202; body=%s", tc.path, rec.Code, rec.Body.String())
		}
		accepted := decodeAsyncAccepted(t, rec.Body)
		tc.wait(accepted.RequestID)

		want := "api: " + tc.op + " prep request_id=" + accepted.RequestID + " session_id=" + info.ID + " total="
		deadline := time.Now().Add(testEventTimeout)
		for !strings.Contains(logs.String(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("logs = %q, want a line containing %q", logs.String(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
		for _, phase := range []string{" wait=", " resolve=", " handle=", " get=", " build="} {
			if !strings.Contains(logs.String(), phase) {
				t.Fatalf("logs = %q, want phase %q", logs.String(), phase)
			}
		}
	}
}
