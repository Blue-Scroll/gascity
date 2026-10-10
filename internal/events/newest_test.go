package events

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readNewest(t *testing.T, path string, filter Filter, keep int) ([]uint64, int) {
	t.Helper()
	got, seen, err := ReadNewestWithInFlight(context.Background(), path, filter, keep)
	if err != nil {
		t.Fatalf("ReadNewestWithInFlight(keep=%d): %v", keep, err)
	}
	return seqsOf(got), seen
}

// TestReadNewestKeepsTheSameTailAsTheListRead pins the newest-first read to
// the list read it stands in for: for every page size, the same newest seqs in
// the same order, each once, across a .gz, a rotating file whose .gz also
// exists, and the active file.
func TestReadNewestKeepsTheSameTailAsTheListRead(t *testing.T) {
	path := seedWalkLog(t)
	all := []uint64{1, 2, 3, 4, 5}
	for keep := 1; keep <= 7; keep++ {
		want := all
		if keep < len(all) {
			want = all[len(all)-keep:]
		}
		got, _ := readNewest(t, path, Filter{}, keep)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("keep=%d: seqs = %v, want %v", keep, got, want)
		}
	}
}

func TestReadNewestAppliesTheFilter(t *testing.T) {
	path := seedWalkLog(t)

	if got, _ := readNewest(t, path, Filter{AfterSeq: 1, BeforeSeq: 5}, 10); !reflect.DeepEqual(got, []uint64{2, 3, 4}) {
		t.Fatalf("seq window = %v, want [2 3 4]", got)
	}
	if got, seen := readNewest(t, path, Filter{Subject: "s3"}, 10); !reflect.DeepEqual(got, []uint64{3}) || seen != 1 {
		t.Fatalf("subject read = %v (seen %d), want [3] (seen 1)", got, seen)
	}
}

// TestReadNewestCountsAnOverlappedSeqOnce covers two archive windows that share
// an edge seq, as the town's real archives do (seq-1-161675 then
// seq-161671-439505).
func TestReadNewestCountsAnOverlappedSeqOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	archiveSeqs(t, dir, 1, 3)
	archiveSeqs(t, dir, 3, 5)
	writeJSONLEvents(t, path, 6)

	got, seen := readNewest(t, path, Filter{}, 10)
	if want := []uint64{1, 2, 3, 4, 5, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("seqs = %v, want %v", got, want)
	}
	if seen != 6 {
		t.Fatalf("seen = %d, want 6", seen)
	}
}

// TestReadNewestStopsOnceItHoldsAPage is the CPU contract. The oldest archive
// is corrupt, so any read that reaches it fails: a page the newer segments can
// fill must never open it.
func TestReadNewestStopsOnceItHoldsAPage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	corruptArchive(t, dir, 1, 10)
	archiveSeqs(t, dir, 11, 20)
	writeJSONLEvents(t, path, seqRange(21, 25)...)

	if got, _ := readNewest(t, path, Filter{}, 3); !reflect.DeepEqual(got, []uint64{23, 24, 25}) {
		t.Fatalf("active-only page = %v, want [23 24 25]", got)
	}
	if got, _ := readNewest(t, path, Filter{}, 15); !reflect.DeepEqual(got, seqRange(11, 25)) {
		t.Fatalf("two-segment page = %v, want 11..25", got)
	}
	if got, _ := readNewest(t, path, Filter{BeforeSeq: 21}, 10); !reflect.DeepEqual(got, seqRange(11, 20)) {
		t.Fatalf("cursor page = %v, want 11..20", got)
	}
	if _, _, err := ReadNewestWithInFlight(context.Background(), path, Filter{}, 16); err == nil {
		t.Fatal("a page that needs the corrupt archive read without an error; the fixture proves nothing")
	}
}

// TestReadNewestStopsAtASegmentOlderThanSince: the phone asks for since=24h.
// Once a whole segment is older than that, every older segment is too, so a
// selective filter must not walk on to the oldest archive.
func TestReadNewestStopsAtASegmentOlderThanSince(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	now := time.Now().UTC()
	corruptArchive(t, dir, 1, 10)
	archiveTimed(t, dir, now.Add(-72*time.Hour), 11, 20)
	writeTimedEvents(t, path, now.Add(-time.Hour), 21, 25)

	got, seen, err := ReadNewestWithInFlight(context.Background(), path, Filter{Since: now.Add(-24 * time.Hour)}, 100)
	if err != nil {
		t.Fatalf("ReadNewestWithInFlight: %v", err)
	}
	if !reflect.DeepEqual(seqsOf(got), seqRange(21, 25)) || seen != 5 {
		t.Fatalf("since page = %v (seen %d), want 21..25 (seen 5)", seqsOf(got), seen)
	}
}

func TestReadNewestStopsWhenTheCallerIsGone(t *testing.T) {
	path := seedWalkLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := ReadNewestWithInFlight(ctx, path, Filter{}, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestReadNewestStopsMidSegmentWhenTheCallerIsGone: one archive can hold
// 350,000 events, so a gone caller must stop inside it, not at its end.
func TestReadNewestStopsMidSegmentWhenTheCallerIsGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeJSONLEvents(t, path, seqRange(1, 3*newestCtxCheckLines)...)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // test file
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	r, err := activeSegmentReader(f, info.Size(), 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	floor := ^uint64(0)
	seg, err := scanSegmentNewest(ctx, r, Filter{}, &floor, 5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if seg.lines >= 3*newestCtxCheckLines {
		t.Fatalf("read all %d lines after the caller left", seg.lines)
	}
	// A read that stopped early saw part of the segment, so its type set must
	// never be taken for the whole archive's (archiveTypes would then skip an
	// archive that holds a match).
	if seg.complete {
		t.Fatal("a canceled scan reported itself complete")
	}
}

func TestReadNewestRefusesALimit(t *testing.T) {
	path := seedWalkLog(t)
	if _, _, err := ReadNewestWithInFlight(context.Background(), path, Filter{Limit: 2}, 3); err == nil {
		t.Fatal("a read with Filter.Limit ran; it must refuse so callers pass keep")
	}
}

func seqRange(first, last uint64) []uint64 {
	out := make([]uint64, 0, last-first+1)
	for s := first; s <= last; s++ {
		out = append(out, s)
	}
	return out
}

// archiveSeqs writes seqs first..last as a canonical .gz archive in dir.
func archiveSeqs(t *testing.T, dir string, first, last uint64) {
	t.Helper()
	src := filepath.Join(dir, fmt.Sprintf("src-%d-%d", first, last))
	writeJSONLEvents(t, src, seqRange(first, last)...)
	gzipInto(t, src, dir, first, last)
}

// archiveTimed is archiveSeqs with every event stamped ts.
func archiveTimed(t *testing.T, dir string, ts time.Time, first, last uint64) {
	t.Helper()
	src := filepath.Join(dir, fmt.Sprintf("src-%d-%d", first, last))
	writeTimedEvents(t, src, ts, first, last)
	gzipInto(t, src, dir, first, last)
}

func gzipInto(t *testing.T, src, dir string, first, last uint64) {
	t.Helper()
	var stderr bytes.Buffer
	dest := filepath.Join(dir, formatArchiveBasename(time.Date(2026, 5, 7, 12, 0, 0, int(first), time.UTC), first, last))
	if err := gzipAndArchive(src, dest, &stderr); err != nil {
		t.Fatalf("gzipAndArchive: %v", err)
	}
}

// corruptArchive puts a file that is not gzip under a canonical archive name,
// so any read that opens it fails.
func corruptArchive(t *testing.T, dir string, first, last uint64) {
	t.Helper()
	bad := filepath.Join(dir, formatArchiveBasename(time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC), first, last))
	if err := os.WriteFile(bad, []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTimedEvents(t *testing.T, path string, ts time.Time, first, last uint64) {
	t.Helper()
	var b strings.Builder
	for s := first; s <= last; s++ {
		fmt.Fprintf(&b, `{"seq":%d,"type":%q,"ts":%q,"subject":"s%d"}`+"\n", s, string(BeadCreated), ts.Format(time.RFC3339Nano), s)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
