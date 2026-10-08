package events

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// walkSeqs walks path and returns every delivered seq, in delivery order.
func walkSeqs(t *testing.T, path string, filter Filter) []uint64 {
	t.Helper()
	var got []uint64
	if err := WalkWithInFlight(path, filter, func(batch []Event) bool {
		got = append(got, seqsOf(batch)...)
		return true
	}); err != nil {
		t.Fatalf("WalkWithInFlight: %v", err)
	}
	return got
}

// seedWalkLog builds the three kinds of segment a walk must cover: a .gz
// archive (seq 1), an in-flight rotating file whose .gz ALSO exists (seq 2-3,
// the promotion overlap), and the active file (seq 4-5).
func seedWalkLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	var stderr bytes.Buffer

	src := filepath.Join(dir, "events.jsonl.rotating-20260507T120000Z-seq-1-1")
	writeJSONLEvents(t, src, 1)
	if err := gzipAndArchive(src, filepath.Join(dir, formatArchiveBasename(time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC), 1, 1)), &stderr); err != nil {
		t.Fatalf("gzipAndArchive: %v", err)
	}

	// Leave the rotating source in place next to its archive: both hold 2-3.
	twin := filepath.Join(dir, "events.jsonl.rotating-20260507T120500Z-seq-2-3")
	writeJSONLEvents(t, twin, 2, 3)
	copySrc := filepath.Join(dir, "copy-of-twin")
	writeJSONLEvents(t, copySrc, 2, 3)
	if err := gzipAndArchive(copySrc, filepath.Join(dir, formatArchiveBasename(time.Date(2026, 5, 7, 12, 5, 0, 0, time.UTC), 2, 3)), &stderr); err != nil {
		t.Fatalf("gzipAndArchive: %v", err)
	}

	writeJSONLEvents(t, path, 4, 5)
	return path
}

// TestWalkWithInFlightCoversTheSameHistoryAsTheListRead pins the walk to the
// read it replaces: same events, same seq order, each seq once.
func TestWalkWithInFlightCoversTheSameHistoryAsTheListRead(t *testing.T) {
	path := seedWalkLog(t)

	listed, err := ReadFilteredWithInFlight(path, Filter{})
	if err != nil {
		t.Fatalf("ReadFilteredWithInFlight: %v", err)
	}
	want := []uint64{1, 2, 3, 4, 5}
	if got := seqsOf(listed); !reflect.DeepEqual(got, want) {
		t.Fatalf("list read seqs = %v, want %v", got, want)
	}
	if got := walkSeqs(t, path, Filter{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("walk seqs = %v, want %v", got, want)
	}
}

func TestWalkWithInFlightAppliesTheFilter(t *testing.T) {
	path := seedWalkLog(t)

	if got := walkSeqs(t, path, Filter{AfterSeq: 1, BeforeSeq: 5}); !reflect.DeepEqual(got, []uint64{2, 3, 4}) {
		t.Fatalf("seq window walk = %v, want [2 3 4]", got)
	}
	if got := walkSeqs(t, path, Filter{Subject: "s3"}); !reflect.DeepEqual(got, []uint64{3}) {
		t.Fatalf("subject walk = %v, want [3]", got)
	}
}

// TestWalkWithInFlightSkipsArchivesPastBeforeSeq proves an archive wholly at or
// above BeforeSeq is never opened: a corrupt one there must not fail the walk.
func TestWalkWithInFlightSkipsArchivesPastBeforeSeq(t *testing.T) {
	path := seedWalkLog(t)
	bad := filepath.Join(filepath.Dir(path), formatArchiveBasename(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC), 10, 20))
	if err := os.WriteFile(bad, []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := walkSeqs(t, path, Filter{BeforeSeq: 4}); !reflect.DeepEqual(got, []uint64{1, 2, 3}) {
		t.Fatalf("walk below 4 = %v, want [1 2 3]", got)
	}
	if err := WalkWithInFlight(path, Filter{}, func([]Event) bool { return true }); err == nil {
		t.Fatal("a full walk read the corrupt archive without an error")
	}
}

// TestWalkWithInFlightHandsOutFreshBoundedBatches is the memory contract: no
// batch is larger than walkBatch, and a batch fn already holds is never
// overwritten by the next one.
func TestWalkWithInFlightHandsOutFreshBoundedBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	const total = walkBatch*2 + 17
	seqs := make([]uint64, total)
	for i := range seqs {
		seqs[i] = uint64(i + 1)
	}
	writeJSONLEvents(t, path, seqs...)

	var batches [][]Event
	if err := WalkWithInFlight(path, Filter{}, func(batch []Event) bool {
		if len(batch) == 0 || len(batch) > walkBatch {
			t.Fatalf("batch size %d, want 1..%d", len(batch), walkBatch)
		}
		batches = append(batches, batch)
		return true
	}); err != nil {
		t.Fatalf("WalkWithInFlight: %v", err)
	}
	var got []uint64
	for _, b := range batches {
		got = append(got, seqsOf(b)...)
	}
	if !reflect.DeepEqual(got, seqs) {
		t.Fatalf("kept batches hold %d seqs, want 1..%d in order", len(got), total)
	}
}

func TestWalkWithInFlightStopsWhenFnSaysSo(t *testing.T) {
	path := seedWalkLog(t)
	calls := 0
	if err := WalkWithInFlight(path, Filter{}, func([]Event) bool {
		calls++
		return false
	}); err != nil {
		t.Fatalf("WalkWithInFlight: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times after asking to stop, want 1", calls)
	}
}

func TestWalkWithInFlightRefusesALimit(t *testing.T) {
	path := seedWalkLog(t)
	if err := WalkWithInFlight(path, Filter{Limit: 2}, func([]Event) bool { return true }); err == nil {
		t.Fatal("a walk with Filter.Limit ran; it must refuse so callers stop from fn")
	}
}

func TestWalkWithInFlightReadsArchivesWithNoActiveFile(t *testing.T) {
	path := seedWalkLog(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := walkSeqs(t, path, Filter{}); !reflect.DeepEqual(got, []uint64{1, 2, 3}) {
		t.Fatalf("walk with no active file = %v, want [1 2 3]", got)
	}
}
