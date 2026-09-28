package api

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/events"
)

// batchWalker hands out seqs 1..n in batches of size, like a FileRecorder walk.
type batchWalker struct {
	n, size int
	err     error
}

func (w batchWalker) WalkInFlight(_ events.Filter, fn func([]events.Event) bool) error {
	for start := 1; start <= w.n; start += w.size {
		var batch []events.Event
		for s := start; s < start+w.size && s <= w.n; s++ {
			batch = append(batch, events.Event{Seq: uint64(s)})
		}
		if !fn(batch) {
			return nil
		}
	}
	return w.err
}

func eventSeqs(evts []events.Event) []uint64 {
	out := make([]uint64, 0, len(evts))
	for _, e := range evts {
		out = append(out, e.Seq)
	}
	return out
}

func TestLastMatchingEventsKeepsOnlyTheNewestInOrder(t *testing.T) {
	cases := []struct {
		name        string
		n, size     int
		keep        int
		want        []uint64
		wantScanned int
	}{
		{name: "wraps across batches", n: 10, size: 3, keep: 4, want: []uint64{7, 8, 9, 10}, wantScanned: 10},
		{name: "ring lands back on slot zero", n: 8, size: 3, keep: 4, want: []uint64{5, 6, 7, 8}, wantScanned: 8},
		{name: "fewer matches than keep", n: 3, size: 2, keep: 5, want: []uint64{1, 2, 3}, wantScanned: 3},
		{name: "no matches", n: 0, size: 2, keep: 5, want: []uint64{}, wantScanned: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, scanned, err := lastMatchingEvents(batchWalker{n: tc.n, size: tc.size}, events.Filter{}, tc.keep)
			if err != nil {
				t.Fatalf("lastMatchingEvents: %v", err)
			}
			if !reflect.DeepEqual(eventSeqs(got), tc.want) {
				t.Fatalf("kept seqs = %v, want %v", eventSeqs(got), tc.want)
			}
			if scanned != tc.wantScanned {
				t.Fatalf("scanned = %d, want %d", scanned, tc.wantScanned)
			}
		})
	}
}

func TestLastMatchingEventsReturnsTheWalkError(t *testing.T) {
	want := errors.New("archive unreadable")
	if _, _, err := lastMatchingEvents(batchWalker{n: 4, size: 2, err: want}, events.Filter{}, 3); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
