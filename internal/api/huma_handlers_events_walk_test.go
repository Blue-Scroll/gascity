package api

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/events"
)

// TestEventListWalksARealRecorderAcrossItsArchive drives the /events page
// fallback through a real FileRecorder, so the NewestProvider branch runs: the
// fakes the other keyset tests use do not implement it. The first page cannot
// fill from the active file alone, so it must read the .gz archive AND the
// active file, and the full walk must see every seq exactly once.
func TestEventListWalksARealRecorderAcrossItsArchive(t *testing.T) {
	state := newFakeState(t)
	rec, err := events.NewFileRecorder(filepath.Join(t.TempDir(), "events.jsonl"), io.Discard)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	state.eventProv = rec
	for i := 0; i < 12; i++ {
		rec.Record(events.Event{Type: "e.t", Actor: "a"})
	}
	if _, err := rec.ForceRotate(); err != nil {
		t.Fatalf("ForceRotate: %v", err)
	}
	rec.WaitForRotations()
	for i := 0; i < 3; i++ {
		rec.Record(events.Event{Type: "e.t", Actor: "a"})
	}
	latest, err := rec.LatestSeq()
	if err != nil {
		t.Fatalf("LatestSeq: %v", err)
	}
	h := newTestCityHandler(t, state)

	seen := map[uint64]int{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("walk did not terminate")
		}
		url := cityURL(state, "/events?limit=5")
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		items, _, next := decodeEventList(t, getList(t, h, url))
		if pages == 0 && (len(items) != 5 || items[0].Seq != latest) {
			t.Fatalf("page 1 = %d items from seq %d, want 5 from the newest seq %d", len(items), firstSeq(items), latest)
		}
		for _, e := range items {
			seen[e.Seq]++
		}
		if next == "" {
			break
		}
		cursor = next
	}
	for seq := uint64(1); seq <= latest; seq++ {
		if seen[seq] != 1 {
			t.Errorf("seq %d seen %d times, want exactly 1", seq, seen[seq])
		}
	}
}

func firstSeq(items []WireEvent) uint64 {
	if len(items) == 0 {
		return 0
	}
	return items[0].Seq
}

// TestEventPageFallbackStopsWhenTheClientIsGone: the fallback must read with
// the request's ctx. The old walk ignored it and ran on for over an hour after
// the phone gave up, one CPU core per abandoned request (hq-bshybj).
func TestEventPageFallbackStopsWhenTheClientIsGone(t *testing.T) {
	rec, err := events.NewFileRecorder(filepath.Join(t.TempDir(), "events.jsonl"), io.Discard)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	for i := 0; i < 3; i++ {
		rec.Record(events.Event{Type: "e.t", Actor: "a"})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A limit past the history makes the tail come up short, so the
	// fallback runs.
	if _, _, err := fetchEventPageAscending(ctx, rec, events.Filter{}, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
