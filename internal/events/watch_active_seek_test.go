package events

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"
)

// TestActiveSegmentReaderStartsAboveCursor pins the active leg's seek: the
// reader must begin at the first line above the cursor, so a near-head resume
// decodes only the lines it will deliver (vn-0o1qyss). Reading from 0 was
// correct but decoded the whole active file while holding a backfill slot.
func TestActiveSegmentReaderStartsAboveCursor(t *testing.T) {
	const n = 500
	path := writeSeqLog(t, n)
	for _, cursor := range []uint64{0, 1, 250, 499, 500, 600} {
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		st, err := f.Stat()
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		sr, err := activeSegmentReader(f, st.Size(), cursor)
		if err != nil {
			t.Fatalf("cursor %d: activeSegmentReader: %v", cursor, err)
		}
		var first uint64
		lines := 0
		for {
			line, rerr := sr.br.ReadBytes('\n')
			if len(line) > 0 {
				var e Event
				if json.Unmarshal(trimLine(line), &e) != nil {
					t.Fatalf("cursor %d: line %d is not a whole event: %q", cursor, lines, line)
				}
				if lines == 0 {
					first = e.Seq
				}
				lines++
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				t.Fatalf("cursor %d: read: %v", cursor, rerr)
			}
		}
		_ = f.Close()
		want := 0
		if cursor < n {
			want = n - int(cursor)
		}
		if lines != want {
			t.Errorf("cursor %d: the reader yielded %d lines, want %d (only the lines above the cursor)", cursor, lines, want)
		}
		if want > 0 && first != cursor+1 {
			t.Errorf("cursor %d: the first line is seq %d, want %d", cursor, first, cursor+1)
		}
		if cursor == 0 && sr.start != 0 {
			t.Errorf("cursor 0 must read from byte 0, started at %d", sr.start)
		}
	}
}

// TestWatchNearHeadResumeSeeksTheActiveLeg proves the watcher hands its cursor
// to the active leg: a resume from seq 100 of 1000 opens that leg at the line
// above 100, not at byte 0, and still delivers every event after the cursor
// exactly once.
func TestWatchNearHeadResumeSeeksTheActiveLeg(t *testing.T) {
	const n = 1000
	path := writeSeqLog(t, n)
	r, err := NewFileRecorder(path, io.Discard, WithoutStartupSweep())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	defer r.Close() //nolint:errcheck // test cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	w, err := r.Watch(ctx, 100)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer w.Close() //nolint:errcheck // test cleanup
	e, err := w.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if e.Seq != 101 {
		t.Fatalf("first event is seq %d, want 101", e.Seq)
	}
	// 900 events remain and one batch holds 256, so the active-leg reader is
	// still open here and says where it started.
	fw := w.(*fileWatcher)
	if fw.bfReader == nil {
		t.Fatal("the active-leg reader closed after one batch; the test needs more than one batch above the cursor")
	}
	if fw.bfReader.start == 0 {
		t.Fatal("the active leg started at byte 0: the watcher did not pass its cursor to activeSegmentReader")
	}
	want := uint64(102)
	for want <= n {
		e, err := w.Next()
		if err != nil {
			t.Fatalf("next at %d: %v", want, err)
		}
		if e.Seq != want {
			t.Fatalf("got seq %d, want %d (no gap, no duplicate)", e.Seq, want)
		}
		want++
	}
}

// TestWatchCursorAtHeadDeliversOnlyNewEvents covers the boundary the seek must
// not cross: a cursor at the last line reads nothing old, and the next event
// recorded is the next one delivered.
func TestWatchCursorAtHeadDeliversOnlyNewEvents(t *testing.T) {
	const n = 300
	path := writeSeqLog(t, n)
	r, err := NewFileRecorder(path, io.Discard, WithoutStartupSweep())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	defer r.Close() //nolint:errcheck // test cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := r.Watch(ctx, n-1)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer w.Close() //nolint:errcheck // test cleanup
	e, err := w.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if e.Seq != n {
		t.Fatalf("first event is seq %d, want %d", e.Seq, n)
	}
	r.Record(Event{Type: "a", Actor: "t"})
	e, err = w.Next()
	if err != nil {
		t.Fatalf("next after record: %v", err)
	}
	if e.Seq != n+1 {
		t.Fatalf("the recorded event arrived as seq %d, want %d", e.Seq, n+1)
	}
}
