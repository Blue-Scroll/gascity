package orders

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

type rowsErrorStore struct {
	*beads.MemStore
	rows []beads.Bead
	err  error
}

func (s *rowsErrorStore) List(_ beads.ListQuery) ([]beads.Bead, error) {
	return s.rows, s.err
}

func ordersStoreOver(store beads.Store) *Store {
	return NewStore(beads.OrdersStore{Store: store})
}

func TestLastRunReturnsLatestRun(t *testing.T) {
	store := beads.NewMemStore()

	first, err := store.Create(beads.Bead{
		Title:  "order:digest",
		Status: "closed",
		Labels: []string{"order-run:digest"},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Millisecond)

	second, err := store.Create(beads.Bead{
		Title:  "order:digest",
		Status: "closed",
		Labels: []string{"order-run:digest", "wisp-failed"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := ordersStoreOver(store).LastRun("digest")
	if err != nil {
		t.Fatalf("LastRun(): %v", err)
	}
	if !got.Equal(second.CreatedAt) {
		t.Fatalf("LastRun() = %s, want %s (latest run should remain authoritative)", got, second.CreatedAt)
	}
	if !second.CreatedAt.After(first.CreatedAt) {
		t.Fatalf("test setup invalid: second.CreatedAt=%s, first.CreatedAt=%s", second.CreatedAt, first.CreatedAt)
	}
}

func TestLastRunReturnsZeroWhenNoRunsExist(t *testing.T) {
	store := beads.NewMemStore()

	got, err := ordersStoreOver(store).LastRun("digest")
	if err != nil {
		t.Fatalf("LastRun(): %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("LastRun() = %s, want zero time", got)
	}
}

func TestLastRunUsesRowsFromPartialTierError(t *testing.T) {
	want := time.Date(2026, 5, 15, 7, 0, 0, 0, time.UTC)
	store := &rowsErrorStore{
		MemStore: beads.NewMemStore(),
		rows: []beads.Bead{{
			ID:        "run-1",
			Title:     "digest",
			CreatedAt: want,
			Labels:    []string{"order-run:digest"},
		}},
		err: errors.New("wisps tier unavailable"),
	}

	got, err := ordersStoreOver(store).LastRun("digest")
	if err != nil {
		t.Fatalf("LastRun(): %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("LastRun() = %s, want %s from surviving rows", got, want)
	}
}

// fallbackSince is a since bound older than every event these tests record.
var fallbackSince = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func TestLastRunFuncWithEventFallback_UsesStoreResult(t *testing.T) {
	want := time.Date(2026, 6, 9, 0, 23, 0, 0, time.UTC)
	storeFn := func(string) (time.Time, error) { return want, nil }
	ep := events.NewFake()
	// Event with a later timestamp — must NOT be chosen when store succeeds.
	ep.Record(events.Event{
		Type:    events.OrderFired,
		Subject: "digest-generate",
		Ts:      want.Add(time.Hour),
	})

	got, err := LastRunFuncWithEventFallback(storeFn, ep, fallbackSince)("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("got %s, want store result %s", got, want)
	}
}

func TestLastRunFuncWithEventFallback_FallsBackToEvents(t *testing.T) {
	storeFn := func(string) (time.Time, error) { return time.Time{}, nil }
	ep := events.NewFake()
	want := time.Date(2026, 6, 9, 0, 23, 0, 0, time.UTC)
	ep.Record(events.Event{
		Type:    events.OrderFired,
		Subject: "digest-generate",
		Ts:      want,
	})

	got, err := LastRunFuncWithEventFallback(storeFn, ep, fallbackSince)("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("got %s, want event timestamp %s", got, want)
	}
}

func TestLastRunFuncWithEventFallback_FallsBackToLatestEvent(t *testing.T) {
	storeFn := func(string) (time.Time, error) { return time.Time{}, nil }
	ep := events.NewFake()
	older := time.Date(2026, 6, 8, 0, 23, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 9, 0, 23, 0, 0, time.UTC)
	ep.Record(events.Event{Type: events.OrderFired, Subject: "digest-generate", Ts: older})
	ep.Record(events.Event{Type: events.OrderFired, Subject: "digest-generate", Ts: newer})

	got, err := LastRunFuncWithEventFallback(storeFn, ep, fallbackSince)("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(newer) {
		t.Fatalf("got %s, want newest event %s", got, newer)
	}
}

func TestLastRunFuncWithEventFallback_IgnoresOtherOrderEvents(t *testing.T) {
	storeFn := func(string) (time.Time, error) { return time.Time{}, nil }
	ep := events.NewFake()
	ep.Record(events.Event{
		Type:    events.OrderFired,
		Subject: "other-order",
		Ts:      time.Date(2026, 6, 9, 0, 23, 0, 0, time.UTC),
	})

	got, err := LastRunFuncWithEventFallback(storeFn, ep, fallbackSince)("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("got %s, want zero (event is for a different order)", got)
	}
}

func TestLastRunFuncWithEventFallback_NilProviderReturnsStoreResult(t *testing.T) {
	want := time.Date(2026, 6, 9, 0, 23, 0, 0, time.UTC)
	storeFn := func(string) (time.Time, error) { return want, nil }

	got, err := LastRunFuncWithEventFallback(storeFn, nil, fallbackSince)("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestLastRunFuncWithEventFallback_NilProviderZeroStore(t *testing.T) {
	storeFn := func(string) (time.Time, error) { return time.Time{}, nil }

	got, err := LastRunFuncWithEventFallback(storeFn, nil, fallbackSince)("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("got %s, want zero (nil provider, no store result)", got)
	}
}

func TestLastRunFuncWithEventFallback_StoreErrorPropagated(t *testing.T) {
	wantErr := errors.New("store error")
	storeFn := func(string) (time.Time, error) { return time.Time{}, wantErr }
	ep := events.NewFake()

	_, err := LastRunFuncWithEventFallback(storeFn, ep, fallbackSince)("digest-generate")
	if !errors.Is(err, wantErr) {
		t.Fatalf("got err %v, want %v", err, wantErr)
	}
}

func TestLastRunFuncWithEventFallback_EventErrorFailsOpen(t *testing.T) {
	storeFn := func(string) (time.Time, error) { return time.Time{}, nil }
	ep := events.NewFailFake()

	got, err := LastRunFuncWithEventFallback(storeFn, ep, fallbackSince)("digest-generate")
	if err != nil {
		t.Fatalf("expected nil error (fail-open), got: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("got %s, want zero time (fail-open on broken provider)", got)
	}
}

func TestCursorUsesRowsAndLogsPartialTierError(t *testing.T) {
	oldLogf := runtimeHelpersLogf
	var logs []string
	runtimeHelpersLogf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() {
		runtimeHelpersLogf = oldLogf
	})
	store := &rowsErrorStore{
		MemStore: beads.NewMemStore(),
		rows: []beads.Bead{{
			ID:     "run-1",
			Labels: []string{"order-run:digest", "seq:42"},
		}},
		err: errors.New("wisps tier unavailable"),
	}

	got := ordersStoreOver(store).Cursor("digest")
	if got != 42 {
		t.Fatalf("Cursor() = %d, want 42 from surviving rows", got)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "partially failed") {
		t.Fatalf("logs = %#v, want partial failure log", logs)
	}
}

// TestLastRunAcrossReturnsMaxScope proves the federation helper takes the most
// recent run across scopes.
func TestLastRunAcrossReturnsMaxScope(t *testing.T) {
	early := beads.NewMemStore()
	if _, err := early.Create(beads.Bead{Title: "order:digest", Status: "closed", Labels: []string{"order-run:digest"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	late := beads.NewMemStore()
	lateRun, err := late.Create(beads.Bead{Title: "order:digest", Status: "closed", Labels: []string{"order-run:digest"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := LastRunAcross([]*Store{ordersStoreOver(early), ordersStoreOver(late)})("digest")
	if err != nil {
		t.Fatalf("LastRunAcross(): %v", err)
	}
	if !got.Equal(lateRun.CreatedAt) {
		t.Fatalf("LastRunAcross() = %s, want %s (max across scopes)", got, lateRun.CreatedAt)
	}
}

// closedHistoryFailsStore serves every active-row read and fails, hard, every
// read that includes closed history: the shape of a backing whose closed rows
// are unreachable while the controller's cache still holds the open ones.
type closedHistoryFailsStore struct{ *beads.MemStore }

func (s closedHistoryFailsStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	if q.IncludeClosed {
		return nil, errors.New("closed history unavailable")
	}
	return s.MemStore.List(q)
}

// Over a CachingStore, a hard backing error on the order-run history read must
// stay an error. Kills: a cached read that turns it into a partial result of
// the open rows the cache holds, which LastRun and Cursor would trust as
// surviving rows — a cooldown clock from an open run, a cursor that replays
// consumed events.
func TestLastRunAndCursorKeepAHardBackingErrorThroughACache(t *testing.T) {
	backing := beads.NewMemStore()
	if _, err := backing.Create(beads.Bead{Title: "order:digest", Labels: []string{"order-run:digest", "seq:3"}}); err != nil {
		t.Fatal(err)
	}
	cache := beads.NewCachingStore(closedHistoryFailsStore{backing}, nil)
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	store := ordersStoreOver(cache)
	if got, err := store.LastRun("digest"); err == nil {
		t.Fatalf("LastRun() = %s, nil; a hard backing error was answered from the cache's open rows", got)
	}
	if got := store.Cursor("digest"); got != 0 {
		t.Fatalf("Cursor() = %d, want 0 (unread); a hard backing error was answered from the cache's open rows", got)
	}
}

// An order.fired event older than since reads as never run: the window is the
// whole point of the bound (vn-ngdqasv).
func TestLastRunFuncWithEventFallback_IgnoresEventsBeforeSince(t *testing.T) {
	storeFn := func(string) (time.Time, error) { return time.Time{}, nil }
	ep := events.NewFake()
	since := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	ep.Record(events.Event{Type: events.OrderFired, Subject: "digest-generate", Ts: since.Add(-time.Minute)})

	got, err := LastRunFuncWithEventFallback(storeFn, ep, since)("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("got %s, want zero (the only event is older than since)", got)
	}

	inWindow := since.Add(time.Minute)
	ep.Record(events.Event{Type: events.OrderFired, Subject: "digest-generate", Ts: inWindow})
	got, err = LastRunFuncWithEventFallback(storeFn, ep, since)("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(inWindow) {
		t.Fatalf("got %s, want the in-window event %s", got, inWindow)
	}
}

// The bound must keep the real file reader out of archives rotated before
// since. The old archive here is not gzip at all, so opening it fails the
// whole List, and the fallback would fail open to zero instead of finding the
// run in the active log. That is how the order with no run on record
// gunzipped all 1.3 GB of the town's archives on 2026-10-12 (vn-ngdqasv).
func TestLastRunFuncWithEventFallback_NeverOpensArchivesBeforeSince(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	now := time.Now().UTC()
	lastRun := now.Add(-time.Hour).Truncate(time.Second)

	oldArchive := filepath.Join(dir, "events.jsonl.archive-"+now.Add(-30*24*time.Hour).Format("20060102T150405Z")+"-seq-1-5.gz")
	if err := os.WriteFile(oldArchive, []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"seq":6,"type":%q,"ts":%q,"actor":"controller","subject":"digest-generate"}`+"\n",
		events.OrderFired, lastRun.Format(time.RFC3339Nano))
	if err := os.WriteFile(logPath, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	ep := events.NewReadOnlyFileProvider(logPath, io.Discard)
	storeFn := func(string) (time.Time, error) { return time.Time{}, nil }

	order := Order{Name: "digest-generate", Trigger: "cooldown", Interval: "1h"}
	got, err := LastRunFuncWithEventFallback(storeFn, ep, now.Add(-order.LastRunEventWindow()))("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(lastRun) {
		t.Fatalf("got %s, want the active-log run %s (the 30-day-old archive must not be opened)", got, lastRun)
	}

	// The same read with no bound opens the bad archive and fails open. This
	// proves the fixture really is unreadable, so the pass above is the bound.
	got, err = LastRunFuncWithEventFallback(storeFn, ep, time.Time{})("digest-generate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("unbounded read got %s, want zero (the bad archive should have failed the list)", got)
	}
}

func TestOrderLastRunEventWindow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		order Order
		want  time.Duration
	}{
		{"cron gets the floor", Order{Trigger: "cron", Schedule: "0 8 * * *"}, lastRunEventWindowFloor},
		{"short cooldown gets the floor", Order{Trigger: "cooldown", Interval: "5m"}, lastRunEventWindowFloor},
		{"cooldown longer than the floor gets its interval", Order{Trigger: "cooldown", Interval: "720h"}, 720 * time.Hour},
		{"bad interval gets the floor", Order{Trigger: "cooldown", Interval: "soon"}, lastRunEventWindowFloor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.order.LastRunEventWindow(); got != tc.want {
				t.Fatalf("LastRunEventWindow() = %s, want %s", got, tc.want)
			}
		})
	}
}
