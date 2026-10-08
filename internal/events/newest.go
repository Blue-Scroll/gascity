package events

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"time"
)

// NewestProvider is an optional extension for providers that can read their
// history from the newest segment back. The supervisor's /events page uses it
// when the active-file tail cannot fill a page.
type NewestProvider interface {
	// ListNewest returns the newest keep events that match filter, in
	// ascending seq order, and seen: how many matches it read on the way. It
	// stops as soon as it holds keep matches, so seen is a lower bound on the
	// true match count, not the count itself.
	ListNewest(ctx context.Context, filter Filter, keep int) (evts []Event, seen int, err error)
}

// The /events page falls back to a newest-first read only when its provider
// is a NewestProvider. Drift here would silently put a slower read back.
var _ NewestProvider = (*FileRecorder)(nil)

// newestConcurrency caps how many newest-first reads may decode history at
// once. A read that finds few matches can still reach the oldest archive, and
// on the town Mac that is 8.6M events of gzip and JSON. Without a cap, every
// phone refresh started one more such read, and they piled up (hq-bshybj).
const newestConcurrency = 2

var newestSlots = make(chan struct{}, newestConcurrency)

// newestCtxCheckLines is how many lines a segment read decodes between checks
// of ctx. A read whose caller has gone away must stop within a fraction of a
// second, not finish the archive.
const newestCtxCheckLines = 1024

// ReadNewestWithInFlight returns the newest keep events matching filter, in
// ascending seq order, plus how many matches it read on the way.
//
// It reads the same history as WalkWithInFlight (the active file, any
// in-flight rotating-* file, and the .gz archives) but in the other direction:
// newest segment first, one whole segment at a time, and it stops as soon as
// it holds keep matches. A page near the head reads the active file and maybe
// one archive, never the whole history. It also stops early when:
//
//   - ctx is canceled. A client that gave up no longer costs a CPU core. The
//     old oldest-first walk ran on for over an hour after its client left
//     (hq-bshybj).
//   - filter.Since is set and a whole segment is older than it. Events get
//     their Ts when they are written, in seq order, so every older segment is
//     older still.
//
// It holds at most keep events plus one decoded line. It refuses Filter.Limit:
// pass keep instead.
func ReadNewestWithInFlight(ctx context.Context, path string, filter Filter, keep int) ([]Event, int, error) {
	if filter.Limit > 0 {
		return nil, 0, fmt.Errorf("events: ReadNewestWithInFlight does not take Filter.Limit; pass keep")
	}
	if keep <= 0 {
		return nil, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	select {
	case newestSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	defer func() { <-newestSlots }()

	// Open the active file BEFORE listing the archives, for the same reason
	// WalkWithInFlight does: a rotation in between then shows the segment we
	// already hold as a rotating-* file or .gz, and the floor below drops it,
	// instead of hiding it from both reads.
	active, activeSize, err := openActiveForWalk(path)
	if err != nil {
		return nil, 0, err
	}
	if active != nil {
		defer active.Close() //nolint:errcheck // read-only file
	}
	sources, err := listBackfillSources(filepath.Dir(path), filter.AfterSeq)
	if err != nil {
		return nil, 0, fmt.Errorf("listing event archives: %w", err)
	}

	// floor is the lowest seq any segment read so far has held. Every seq at
	// or above it is already covered, so an older segment only adds seqs
	// below it. That drops the second copy when a .gz and its rotating-*
	// source share a window, or when two archive windows overlap.
	floor := uint64(math.MaxUint64)
	if filter.BeforeSeq > 0 {
		floor = filter.BeforeSeq
	}
	// pages holds each segment's kept matches, newest segment first.
	var pages [][]Event
	held, seen := 0, 0
	read := func(r *segmentReader) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		seg, err := scanSegmentNewest(ctx, r, filter, &floor, keep-held)
		seen += seg.matched
		if len(seg.kept) > 0 {
			pages = append(pages, seg.kept)
			held += len(seg.kept)
		}
		if err != nil {
			return false, err
		}
		if held >= keep {
			return false, nil
		}
		if !filter.Since.IsZero() && seg.lines > 0 && seg.newestTs.Before(filter.Since) {
			return false, nil
		}
		return true, nil
	}

	more := true
	if active != nil {
		r, err := activeSegmentReader(active, activeSize)
		if err != nil {
			return nil, 0, fmt.Errorf("reading events: %w", err)
		}
		if more, err = read(r); err != nil {
			return nil, 0, fmt.Errorf("reading events: %w", err)
		}
	}
	for i := len(sources) - 1; more && i >= 0; i-- {
		src := sources[i]
		// Wholly at or above what is already covered (or the page boundary).
		if src.firstSeq >= floor {
			continue
		}
		r, err := openSegmentReader(src)
		if err != nil {
			return nil, 0, fmt.Errorf("reading %q: %w", filepath.Base(src.path), err)
		}
		if r == nil {
			continue
		}
		more, err = read(r)
		r.close()
		if err != nil {
			return nil, 0, fmt.Errorf("reading %q: %w", filepath.Base(src.path), err)
		}
	}

	out := make([]Event, 0, held)
	for i := len(pages) - 1; i >= 0; i-- {
		out = append(out, pages[i]...)
	}
	if len(out) > keep {
		out = out[len(out)-keep:]
	}
	return out, seen, nil
}

// newestSegment is what one segment read found.
type newestSegment struct {
	kept     []Event   // the newest matches, at most the room asked for, ascending
	matched  int       // every match below the floor, kept or not
	lines    int       // every decoded event below the floor, match or not
	newestTs time.Time // the latest Ts among those lines
}

// scanSegmentNewest reads one segment start to end. A .gz cannot be read
// backward, so the newest matches are kept in a ring of size room as they go
// by. Only events below *floor count, and *floor drops to the lowest seq read.
func scanSegmentNewest(ctx context.Context, r *segmentReader, filter Filter, floor *uint64, room int) (newestSegment, error) {
	var seg newestSegment
	ring := make([]Event, room)
	next := 0
	// ceiling stays fixed for the whole segment: seqs inside one segment never
	// repeat, so only newer segments can have covered a seq already.
	ceiling := *floor
	for n := 1; ; n++ {
		if n%newestCtxCheckLines == 0 {
			if err := ctx.Err(); err != nil {
				return seg, err
			}
		}
		line, err := r.br.ReadBytes('\n')
		if len(line) > 0 {
			var e Event
			if json.Unmarshal(trimLine(line), &e) == nil && e.Seq < ceiling {
				*floor = min(*floor, e.Seq)
				seg.lines++
				if e.Ts.After(seg.newestTs) {
					seg.newestTs = e.Ts
				}
				if matchesFilter(e, filter) {
					ring[next] = e
					next = (next + 1) % room
					seg.matched++
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				return seg, err
			}
			break
		}
	}
	if seg.matched < room {
		seg.kept = ring[:seg.matched]
	} else {
		// The ring is full, so next points at the oldest kept event.
		seg.kept = append(ring[next:], ring[:next]...)
	}
	return seg, nil
}
