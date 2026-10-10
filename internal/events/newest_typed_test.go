package events

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLineHead(t *testing.T) {
	cases := []struct {
		line string
		seq  uint64
		typ  string
		ok   bool
	}{
		{`{"seq":42,"type":"bead.closed","ts":"x"}` + "\n", 42, "bead.closed", true},
		{`{"seq":7,"type":"request.failed"}`, 7, "request.failed", true},
		{`{"seq":7,"type":""}`, 7, "", true},
		{`{"type":"bead.closed","seq":42}`, 0, "", false},         // another field order
		{`{"seq": 42,"type":"bead.closed"}`, 0, "", false},        // a space
		{`{"seq":42, "type":"bead.closed"}`, 0, "", false},        // a space
		{`{"seq":0,"type":"bead.closed"}`, 0, "", false},          // seq 0 is never assigned
		{`{"seq":42,"type":"a\"b"}`, 0, "", false},                // an escape in the type
		{`{"seq":42,"type":"unterminated`, 0, "", false},          // no closing quote
		{`{"seq":42}`, 0, "", false},                              // no type
		{`{"seq":12345678901234567890,"type":"a"}`, 0, "", false}, // too many digits
		{`not json`, 0, "", false},
		{``, 0, "", false},
	}
	for _, c := range cases {
		seq, typ, ok := lineHead([]byte(c.line))
		if ok != c.ok || seq != c.seq || string(typ) != c.typ {
			t.Errorf("lineHead(%q) = (%d, %q, %v), want (%d, %q, %v)", c.line, seq, typ, ok, c.seq, c.typ, c.ok)
		}
	}
}

// resetArchiveTypes empties the per-process archive type memory, so each test
// starts cold. It is package state, so the tests here do not run in parallel.
func resetArchiveTypes(t *testing.T) {
	t.Helper()
	archiveTypes.Lock()
	archiveTypes.m = map[archiveTypesKey]map[string]struct{}{}
	archiveTypes.Unlock()
	t.Cleanup(func() {
		archiveTypes.Lock()
		archiveTypes.m = map[archiveTypesKey]map[string]struct{}{}
		archiveTypes.Unlock()
	})
}

// writeMixedLines writes seqs first..last as lines whose type is typeOf(seq).
// Some lines take the shapes lineHead refuses, so the full decode is proven to
// still find them.
func writeMixedLines(t *testing.T, path string, first, last uint64, typeOf func(uint64) string) {
	t.Helper()
	var b strings.Builder
	for s := first; s <= last; s++ {
		typ := typeOf(s)
		switch {
		case s%17 == 0:
			// Another field order: lineHead says no, so it must be decoded.
			fmt.Fprintf(&b, `{"type":%q,"seq":%d,"actor":"t","subject":"s%d"}`+"\n", typ, s, s)
		case s%13 == 0:
			// Another type, with the wanted type's name in its message.
			fmt.Fprintf(&b, `{"seq":%d,"type":"decoy","actor":"t","message":"mentions \"want\" and want"}`+"\n", s)
		case s%11 == 0:
			b.WriteString("{not json at all\n")
		default:
			fmt.Fprintf(&b, `{"seq":%d,"type":%q,"actor":"t","subject":"s%d"}`+"\n", s, typ, s)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func archiveMixed(t *testing.T, dir string, first, last uint64, typeOf func(uint64) string) string {
	t.Helper()
	src := filepath.Join(dir, fmt.Sprintf("src-%d-%d", first, last))
	writeMixedLines(t, src, first, last, typeOf)
	gzipInto(t, src, dir, first, last)
	return filepath.Join(dir, formatArchiveBasename(time.Date(2026, 5, 7, 12, 0, 0, int(first), time.UTC), first, last))
}

// seedTypedLog: three archives and an active file. "want" lives only in the
// oldest archive and the active file; the middle archive holds none.
func seedTypedLog(t *testing.T) (path, middle string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "events.jsonl")
	archiveMixed(t, dir, 1, 200, func(s uint64) string {
		if s%3 == 0 {
			return "want"
		}
		return "other"
	})
	middle = archiveMixed(t, dir, 201, 400, func(uint64) string { return "other" })
	archiveMixed(t, dir, 401, 600, func(s uint64) string {
		if s == 450 {
			return "rare"
		}
		return "other"
	})
	writeMixedLines(t, path, 601, 650, func(s uint64) string {
		if s%5 == 0 {
			return "want"
		}
		return "other"
	})
	return path, middle
}

// TestReadNewestTypedMatchesTheFullRead pins the line-head skip to the read it
// replaces: for every page size and filter, cold and warm, the newest-first read
// returns the same events as decoding everything.
func TestReadNewestTypedMatchesTheFullRead(t *testing.T) {
	resetArchiveTypes(t)
	path, _ := seedTypedLog(t)
	all, err := ReadFiltered(path, Filter{})
	if err != nil {
		t.Fatalf("ReadFiltered: %v", err)
	}
	filters := []Filter{
		{Type: "want"},
		{Type: "rare"},
		{Type: "decoy"},
		{Type: "absent"},
		{Type: "want", BeforeSeq: 600},
		{Type: "want", Since: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}, // ts is zero, so nothing passes Since
	}
	for pass := 1; pass <= 2; pass++ { // pass 2 runs with the archive memory warm
		for _, f := range filters {
			want := seqsOf(ApplyFilter(all, f))
			for _, keep := range []int{1, 3, 51, 1000} {
				exp := want
				if len(exp) > keep {
					exp = exp[len(exp)-keep:]
				}
				got, _ := readNewest(t, path, f, keep)
				if len(exp) == 0 {
					exp = []uint64{}
				}
				if len(got) == 0 {
					got = []uint64{}
				}
				if !reflect.DeepEqual(got, exp) {
					t.Fatalf("pass %d, filter %+v, keep %d: seqs = %v, want %v", pass, f, keep, got, exp)
				}
			}
		}
	}
}

// TestReadNewestSkipsAnArchiveThatLacksTheType proves the skip: once an archive
// was read through, a typed read for a type it lacks never opens it. The
// archive is made unreadable after the warm-up, so opening it fails.
func TestReadNewestSkipsAnArchiveThatLacksTheType(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file modes that stop an open")
	}
	resetArchiveTypes(t)
	path, middle := seedTypedLog(t)
	want, _ := readNewest(t, path, Filter{Type: "want"}, 1000) // warms all three archives
	if err := os.Chmod(middle, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(middle, 0o644) })

	got, _ := readNewest(t, path, Filter{Type: "want"}, 1000)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("warm read = %v, want %v", got, want)
	}
	// The fixture must prove something: cold, the same read opens the archive
	// and fails.
	resetArchiveTypes(t)
	if _, _, err := ReadNewestWithInFlight(t.Context(), path, Filter{Type: "want"}, 1000); err == nil {
		t.Fatal("a cold read of an unreadable archive succeeded; the skip test proves nothing")
	}
}

// TestReadNewestRereadsAnArchiveReplacedUnderItsName: the memory is keyed by
// size and modification time too, so a file rewritten under the same name is
// read again, never answered from the old set.
func TestReadNewestRereadsAnArchiveReplacedUnderItsName(t *testing.T) {
	resetArchiveTypes(t)
	path, middle := seedTypedLog(t)
	if got, _ := readNewest(t, path, Filter{Type: "late"}, 10); len(got) != 0 {
		t.Fatalf("type late found before it exists: %v", got)
	}
	// Rewrite the middle archive with a "late" event at seq 300.
	dir := filepath.Dir(path)
	_ = os.Remove(middle)
	archiveMixed(t, dir, 201, 400, func(s uint64) string {
		if s == 302 {
			return "late"
		}
		return "other"
	})
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(middle, later, later); err != nil {
		t.Fatal(err)
	}
	if got, _ := readNewest(t, path, Filter{Type: "late"}, 10); !reflect.DeepEqual(got, []uint64{302}) {
		t.Fatalf("after the rewrite, type late = %v, want [302]", got)
	}
}
