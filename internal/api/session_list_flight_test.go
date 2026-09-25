package api

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// gatedSessionReadStore counts the session-bead reads a list build makes and
// holds each one until release is closed.
type gatedSessionReadStore struct {
	beads.Store
	release chan struct{}
	reads   atomic.Int32
}

func (s *gatedSessionReadStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	if query.Label == session.LabelSession || query.Type == session.BeadType {
		s.reads.Add(1)
		<-s.release
	}
	return s.Store.List(query)
}

// A dashboard asks for /sessions again on every session or bead event. On the
// town the copies piled up and each took 15s to 2 minutes, so no copy ever
// answered (hq-subxy4). Identical requests that overlap must share one build.
func TestHandleSessionListSharesOneBuildAcrossConcurrentRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const callers = 8

		fs := newSessionFakeState(t)
		info := createTestSession(t, fs.cityBeadStore, fs.sp, "Session A")
		gate := &gatedSessionReadStore{Store: fs.cityBeadStore, release: make(chan struct{})}
		fs.cityBeadStore = gate
		srv := New(fs)
		input := &SessionListInput{CityScope: CityScope{CityName: fs.cityName}}

		// One request alone, to learn how many session reads one build makes.
		close(gate.release)
		if _, err := srv.humaHandleSessionList(context.Background(), input); err != nil {
			t.Fatalf("solo list: %v", err)
		}
		readsPerBuild := gate.reads.Load()
		if readsPerBuild == 0 {
			t.Fatal("a list build made no session reads; the gate cannot hold it")
		}
		gate.reads.Store(0)
		gate.release = make(chan struct{})

		outs := make([]*ListOutput[sessionResponse], callers)
		for i := range callers {
			go func() {
				out, err := srv.humaHandleSessionList(context.Background(), input)
				if err != nil {
					t.Errorf("caller %d: %v", i, err)
				}
				outs[i] = out
			}()
		}
		// Every caller is now building or waiting on the one build in flight.
		synctest.Wait()
		close(gate.release)
		synctest.Wait()

		if got := gate.reads.Load(); got != readsPerBuild {
			t.Errorf("session reads for %d overlapping requests = %d, want %d (one build)", callers, got, readsPerBuild)
		}
		for i, out := range outs {
			if out == nil || len(out.Body.Items) != 1 || out.Body.Items[0].ID != info.ID {
				t.Fatalf("caller %d got %+v, want the one session %s", i, out, info.ID)
			}
		}
		// Each sharer holds its own copy, so no caller can change another's answer.
		if &outs[0].Body.Items[0] == &outs[1].Body.Items[0] {
			t.Error("two callers share one items array, want a copy each")
		}
	})
}
