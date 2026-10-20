package events

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tailScanFixture writes events to a fresh events.jsonl, one JSON line each,
// with raw lines (garbage, a torn tail) spliced in after the seq named in raw.
func tailScanFixture(t *testing.T, evts []Event, raw map[uint64]string) string {
	t.Helper()
	var buf bytes.Buffer
	for _, e := range evts {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		if r, ok := raw[e.Seq]; ok {
			buf.WriteString(r)
		}
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// paddedEvents returns n events with seqs 1..n, one second apart, each about
// 1 KiB, typed by typeOf.
func paddedEvents(n int, base time.Time, typeOf func(seq uint64) string) []Event {
	pad := strings.Repeat("x", 1000)
	evts := make([]Event, 0, n)
	for i := 1; i <= n; i++ {
		seq := uint64(i)
		evts = append(evts, Event{
			Seq: seq, Type: typeOf(seq), Actor: "api",
			Ts: base.Add(time.Duration(i) * time.Second), Message: pad,
		})
	}
	return evts
}

// TestScanFilteredTailStopsOnBudgetAndSaysWhere is the core of vn-1sn13x0. A
// rare filter must not read the whole file to answer one page. The walk stops
// on MaxScanBytes and says where, and resuming from there finds the match.
func TestScanFilteredTailStopsOnBudgetAndSaysWhere(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	evts := paddedEvents(300, base, func(seq uint64) string {
		if seq == 1 {
			return "rare"
		}
		return "common"
	})
	path := tailScanFixture(t, evts, nil)

	filter := Filter{Type: "rare", MaxScanBytes: 64 * 1024}
	first, err := ScanFilteredTail(path, filter, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 0 {
		t.Fatalf("first scan found %d events, want 0 (the match is far outside the window)", len(first.Events))
	}
	if first.StoppedAt == 0 || first.StoppedAt >= 300 {
		t.Fatalf("StoppedAt = %d, want a seq inside the file: the walk ran out of budget", first.StoppedAt)
	}
	if first.ReachedStart {
		t.Fatal("ReachedStart = true on a walk that stopped on its budget")
	}
	// A 64 KiB window holds about 60 of these 1 KiB lines, so the walk
	// stopped near seq 240, not after reading most of the file.
	if first.StoppedAt < 200 {
		t.Fatalf("StoppedAt = %d, want above 200: the walk read far past its 64 KiB budget", first.StoppedAt)
	}

	var found []Event
	before := uint64(0)
	scan := first
	for pages := 1; ; pages++ {
		if pages > 20 {
			t.Fatal("resumed walk did not finish")
		}
		found = append(found, scan.Events...)
		if scan.StoppedAt == 0 {
			break
		}
		if before != 0 && scan.StoppedAt >= before {
			t.Fatalf("StoppedAt %d did not move below the boundary %d", scan.StoppedAt, before)
		}
		before = scan.StoppedAt
		next := filter
		next.BeforeSeq = before
		if scan, err = ScanFilteredTail(path, next, 5); err != nil {
			t.Fatal(err)
		}
	}
	if len(found) != 1 || found[0].Seq != 1 {
		t.Fatalf("resumed walk found %+v, want only the seq-1 match", found)
	}
	if !scan.ReachedStart {
		t.Fatal("the last scan did not report ReachedStart")
	}
}

// TestScanFilteredTailKeysetWalkSeesEveryMatchOnce walks a file through both
// kinds of page, full ones and budget stops, past garbage lines and a torn last
// line, and must see every match exactly once.
func TestScanFilteredTailKeysetWalkSeesEveryMatchOnce(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	evts := paddedEvents(400, base, func(seq uint64) string {
		if seq%37 == 0 || (seq > 350 && seq%3 == 0) {
			return "want"
		}
		return "other"
	})
	path := tailScanFixture(t, evts, map[uint64]string{
		50:  "not json at all\n",
		51:  "\n\n",
		200: `{"seq":` + "\n",
		400: `{"seq":401,"type":"want","ac`, // torn final append
	})
	expected := map[uint64]bool{}
	for _, e := range evts {
		if e.Type == "want" {
			expected[e.Seq] = true
		}
	}

	seen := map[uint64]int{}
	before := uint64(0)
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatal("walk did not finish")
		}
		scan, err := ScanFilteredTail(path, Filter{Type: "want", BeforeSeq: before, MaxScanBytes: 48 * 1024}, 4)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range scan.Events {
			seen[e.Seq]++
		}
		switch {
		case len(scan.Events) == 4:
			before = scan.Events[0].Seq
		case scan.StoppedAt > 0:
			before = scan.StoppedAt
		default:
			if !scan.ReachedStart {
				t.Fatalf("short page with no StoppedAt and no ReachedStart: %+v", scan)
			}
			before = 0
		}
		if before == 0 {
			break
		}
	}
	for seq := range expected {
		if seen[seq] != 1 {
			t.Errorf("seq %d seen %d times, want 1", seq, seen[seq])
		}
	}
	for seq, n := range seen {
		if !expected[seq] {
			t.Errorf("seq %d seen %d times, want 0 (not a match)", seq, n)
		}
	}
}

// TestTailWalkEndStartsAtTheBoundary pins where a cursor page starts. A deep
// page must not decode every line above its BeforeSeq to get there: that made
// each page of a keyset walk cost more than the one before. The walk must start
// at the boundary line exactly, garbage lines or not.
func TestTailWalkEndStartsAtTheBoundary(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	evts := paddedEvents(2000, base, func(uint64) string { return "e.t" })
	raw := map[uint64]string{}
	for seq := uint64(3); seq < 2000; seq += 97 {
		raw[seq] = "garbage line with no seq\n"
	}
	path := tailScanFixture(t, evts, raw)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // test cleanup

	for _, before := range []uint64{0, 1, 2, 10, 500, 1234, 1999, 2000, 2001, 5000} {
		// want is where the first line at or above before starts.
		want := int64(len(data))
		if before >= 1 && before <= 2000 {
			want = int64(bytes.Index(data, []byte(`{"seq":`+strconv.FormatUint(before, 10)+`,`)))
		}
		if got := tailWalkEnd(f, int64(len(data)), before); got != want {
			t.Errorf("before=%d: walk starts at %d, want %d", before, got, want)
		}
	}
}

// TestTailWalkEndDistrustsAnUnorderedFile: when the file's tail contradicts
// its head, the search cannot be trusted, and the walk starts at the end. A
// wrong guess there would skip lines the walk should read.
func TestTailWalkEndDistrustsAnUnorderedFile(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	evts := paddedEvents(200, base, func(uint64) string { return "e.t" })
	for i := range evts {
		evts[i].Seq += 50 // the file holds seqs 51..250, as after a rotation
	}
	evts = append(evts, Event{Seq: 5, Type: "e.t", Actor: "api", Ts: base}) // a stale writer's append, below the head
	path := tailScanFixture(t, evts, nil)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // test cleanup
	if got := tailWalkEnd(f, info.Size(), 100); got != info.Size() {
		t.Fatalf("walk starts at %d, want the end of the file (%d)", got, info.Size())
	}
}

// TestScanFilteredTailBudgetCountsFromTheCursor: a deep page must not spend its
// budget walking back over lines above its BeforeSeq, or every page past the
// first budget's worth would come back empty.
func TestScanFilteredTailBudgetCountsFromTheCursor(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	evts := paddedEvents(500, base, func(uint64) string { return "e.t" })
	path := tailScanFixture(t, evts, nil)

	scan, err := ScanFilteredTail(path, Filter{BeforeSeq: 10, MaxScanBytes: 64 * 1024}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Events) != 5 {
		t.Fatalf("got %d events, want 5 (the budget was spent above the cursor)", len(scan.Events))
	}
	for i, e := range scan.Events {
		if e.Seq != uint64(5+i) {
			t.Fatalf("event[%d].Seq = %d, want %d", i, e.Seq, 5+i)
		}
	}
	if scan.StoppedAt != 0 {
		t.Fatalf("StoppedAt = %d, want 0 on a full page", scan.StoppedAt)
	}

	// ReadFilteredTail takes the same shortcut and must return the same rows.
	got, err := ReadFilteredTail(path, Filter{BeforeSeq: 10}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0].Seq != 5 || got[4].Seq != 9 {
		t.Fatalf("ReadFilteredTail with BeforeSeq = %+v, want seqs 5..9", got)
	}
}

// TestScanFilteredTailBoundaryEdges: a cursor at or past the newest seq, and
// one below the oldest, both land on the right side of the bisection.
func TestScanFilteredTailBoundaryEdges(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	evts := paddedEvents(300, base, func(uint64) string { return "e.t" })
	path := tailScanFixture(t, evts, nil)

	top, err := ScanFilteredTail(path, Filter{BeforeSeq: 1000}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(top.Events) != 2 || top.Events[1].Seq != 300 {
		t.Fatalf("BeforeSeq past the head = %+v, want seqs 299 and 300", top.Events)
	}

	bottom, err := ScanFilteredTail(path, Filter{BeforeSeq: 1, MaxScanBytes: 64 * 1024}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(bottom.Events) != 0 || bottom.StoppedAt != 0 || !bottom.ReachedStart {
		t.Fatalf("BeforeSeq 1 = %+v, want nothing, no stop, start reached", bottom)
	}
}

// TestScanFilteredTailSinceEndsTheWalk: once a whole budget window is older
// than Since, nothing older can match, so the walk is done. Without this rule a
// --since walk would page through the whole file one empty window at a time.
func TestScanFilteredTailSinceEndsTheWalk(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	evts := paddedEvents(300, base, func(uint64) string { return "e.t" })
	path := tailScanFixture(t, evts, nil)
	cutoff := base.Add(298 * time.Second) // seqs 298..300 are in the window

	filter := Filter{Since: cutoff, MaxScanBytes: 64 * 1024}
	first, err := ScanFilteredTail(path, filter, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 3 {
		t.Fatalf("first scan = %d events, want 3 (seqs 298..300)", len(first.Events))
	}
	if first.StoppedAt == 0 {
		t.Fatal("first scan StoppedAt = 0, want a resume point: its window held matches, so older ones may exist")
	}

	filter.BeforeSeq = first.StoppedAt
	second, err := ScanFilteredTail(path, filter, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 0 || second.StoppedAt != 0 || second.ReachedStart {
		t.Fatalf("second scan = %+v, want done: no events, no resume point, start not reached", second)
	}
}

// TestScanFilteredTailWalksPastLinesWithNoSeq: a budget window that holds no
// line with a seq gives a stopped walk nowhere to resume from, and a page with
// no cursor reads as the end. The walk must go on until it reads a seq.
func TestScanFilteredTailWalksPastLinesWithNoSeq(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	evts := paddedEvents(20, base, func(seq uint64) string {
		if seq == 1 {
			return "rare"
		}
		return "common"
	})
	pad := strings.Repeat("y", 1000)
	var seqless strings.Builder
	for i := 0; i < 100; i++ { // about 100 KiB of lines with no seq at the end
		seqless.WriteString(`{"type":"legacy","actor":"api","message":"` + pad + `"}` + "\n")
	}
	path := tailScanFixture(t, evts, map[uint64]string{20: seqless.String()})

	scan, err := ScanFilteredTail(path, Filter{Type: "rare", MaxScanBytes: 16 * 1024}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if scan.StoppedAt == 0 && !scan.ReachedStart && len(scan.Events) == 0 {
		t.Fatalf("scan = %+v: stopped with no resume point, which reads as the end", scan)
	}
	if scan.StoppedAt != 0 && scan.StoppedAt > 20 {
		t.Fatalf("StoppedAt = %d, want a seq the file holds", scan.StoppedAt)
	}
}

// TestScanFilteredTailRefusesNoLimit: a scan without a limit is a full read,
// which is ReadFiltered's job.
func TestScanFilteredTailRefusesNoLimit(t *testing.T) {
	path := tailScanFixture(t, paddedEvents(3, time.Now(), func(uint64) string { return "e.t" }), nil)
	if _, err := ScanFilteredTail(path, Filter{}, 0); err == nil {
		t.Fatal("ScanFilteredTail with limit 0 returned no error")
	}
}

// TestScanFilteredTailMissingFile reads as empty, like ReadFilteredTail.
func TestScanFilteredTailMissingFile(t *testing.T) {
	scan, err := ScanFilteredTail(filepath.Join(t.TempDir(), "missing.jsonl"), Filter{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Events) != 0 || scan.StoppedAt != 0 {
		t.Fatalf("missing file = %+v, want empty", scan)
	}
}
