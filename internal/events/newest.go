package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
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
	read := func(r *segmentReader) (newestSegment, bool, error) {
		if err := ctx.Err(); err != nil {
			return newestSegment{}, false, err
		}
		seg, err := scanSegmentNewest(ctx, r, filter, &floor, keep-held)
		seen += seg.matched
		if len(seg.kept) > 0 {
			pages = append(pages, seg.kept)
			held += len(seg.kept)
		}
		if err != nil {
			return seg, false, err
		}
		if held >= keep {
			return seg, false, nil
		}
		if !filter.Since.IsZero() && seg.lines > 0 && seg.newestTs.Before(filter.Since) {
			return seg, false, nil
		}
		return seg, true, nil
	}

	more := true
	if active != nil {
		r, err := activeSegmentReader(active, activeSize, filter.AfterSeq)
		if err != nil {
			return nil, 0, fmt.Errorf("reading events: %w", err)
		}
		if _, more, err = read(r); err != nil {
			return nil, 0, fmt.Errorf("reading events: %w", err)
		}
	}
	for i := len(sources) - 1; more && i >= 0; i-- {
		src := sources[i]
		// Wholly at or above what is already covered (or the page boundary).
		if src.firstSeq >= floor {
			continue
		}
		key, keyed := archiveTypesKeyOf(src)
		if keyed && filter.Type != "" && archiveTypesLacks(key, filter.Type) {
			// A whole archive read before holds no event of this type, so it
			// can hold no match. Its window counts as covered, as if read.
			floor = min(floor, src.firstSeq)
			continue
		}
		r, err := openSegmentReader(src)
		if err != nil {
			return nil, 0, fmt.Errorf("reading %q: %w", filepath.Base(src.path), err)
		}
		if r == nil {
			continue
		}
		var seg newestSegment
		seg, more, err = read(r)
		r.close()
		if err != nil {
			return nil, 0, fmt.Errorf("reading %q: %w", filepath.Base(src.path), err)
		}
		if keyed && seg.complete {
			archiveTypesRemember(key, seg.types)
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
	// types is every event type the segment holds, below the floor or not,
	// and complete says the read reached its end, so types is all of them.
	types    map[string]struct{}
	complete bool
}

// archiveTypes remembers, for each archive a newest-first read went all the
// way through, every event type it holds. An archive never changes once it is
// written, so after one full read a typed read can skip every archive whose set
// lacks its type without opening it.
//
// That skip is what keeps a typed list to seconds. A type that is rare in the
// newest segments sends the read deep into history: request.failed and
// request.result.session.submit each made GET /events?type=...&limit=50 gunzip
// all 1.3 GB of the town Mac's archives, and before the line-head read below it
// decoded every line too, so it sent no headers in 300 s (vn-0o1qyss). The
// first such read still pays the full walk once per gc process; every later
// typed read skips the archives it has seen.
//
// The key is the archive's path, size and modification time, so a file that
// is replaced under the same name is read again rather than trusted. Only a
// read that reached the end of the archive is remembered: a canceled read saw
// part of it, and a partial set would skip an archive that holds a match.
var archiveTypes = struct {
	sync.Mutex
	m map[archiveTypesKey]map[string]struct{}
}{m: map[archiveTypesKey]map[string]struct{}{}}

// archiveTypesMax bounds the memory: about one entry per archive retention
// keeps (43 on the town Mac). Past it the memory starts over, which costs one
// more full read, never a wrong answer.
const archiveTypesMax = 1024

type archiveTypesKey struct {
	path    string
	size    int64
	modNano int64
}

// archiveTypesKeyOf keys a canonical .gz archive. A rotating-* file is not an
// archive yet (it can still be promoted under another name), so it is never
// keyed.
func archiveTypesKeyOf(src backfillSource) (archiveTypesKey, bool) {
	if src.kind != sourceArchive {
		return archiveTypesKey{}, false
	}
	info, err := os.Stat(src.path)
	if err != nil {
		return archiveTypesKey{}, false
	}
	return archiveTypesKey{path: src.path, size: info.Size(), modNano: info.ModTime().UnixNano()}, true
}

// archiveTypesLacks reports whether the archive was read through before and
// holds no event of type typ. An archive never read through answers false.
func archiveTypesLacks(key archiveTypesKey, typ string) bool {
	archiveTypes.Lock()
	defer archiveTypes.Unlock()
	types, ok := archiveTypes.m[key]
	if !ok {
		return false
	}
	_, has := types[typ]
	return !has
}

func archiveTypesRemember(key archiveTypesKey, types map[string]struct{}) {
	archiveTypes.Lock()
	defer archiveTypes.Unlock()
	if len(archiveTypes.m) >= archiveTypesMax {
		archiveTypes.m = map[archiveTypesKey]map[string]struct{}{}
	}
	archiveTypes.m[key] = types
}

// lineHead reads the seq and the raw type of a line written the way
// FileRecorder writes every line, {"seq":N,"type":"T",... (Event's first two
// fields, which encoding/json writes in order with no spaces), without decoding
// the rest. ok is false for any other shape, and for a type that holds an
// escape, and the caller then decodes the line in full, so an unusual line
// costs time, never correctness.
func lineHead(line []byte) (seq uint64, typ []byte, ok bool) {
	const seqKey, typeKey = `{"seq":`, `,"type":"`
	if !bytes.HasPrefix(line, []byte(seqKey)) {
		return 0, nil, false
	}
	i := len(seqKey)
	start := i
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		if i-start >= 19 { // past any seq a recorder can assign
			return 0, nil, false
		}
		seq = seq*10 + uint64(line[i]-'0')
		i++
	}
	if i == start || seq == 0 || !bytes.HasPrefix(line[i:], []byte(typeKey)) {
		return 0, nil, false
	}
	rest := line[i+len(typeKey):]
	end := bytes.IndexByte(rest, '"')
	if end < 0 || bytes.IndexByte(rest[:end], '\\') >= 0 {
		return 0, nil, false
	}
	return seq, rest[:end], true
}

// scanSegmentNewest reads one segment start to end. A .gz cannot be read
// backward, so the newest matches are kept in a ring of size room as they go
// by. Only events below *floor count, and *floor drops to the lowest seq read.
//
// A typed read with no Since decodes only the lines of its type: lineHead reads
// a line's seq and type without decoding it, and a line of another type still
// lowers *floor and counts as read. Every line's type also goes into seg.types,
// which archiveTypes keeps once the read reaches the end.
func scanSegmentNewest(ctx context.Context, r *segmentReader, filter Filter, floor *uint64, room int) (newestSegment, error) {
	seg := newestSegment{types: map[string]struct{}{}}
	// Since needs every line's ts, so only a read without it may skip decodes.
	skipOthers := filter.Type != "" && filter.Since.IsZero()
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
			if seq, typ, ok := lineHead(line); ok {
				if _, seenType := seg.types[string(typ)]; !seenType {
					seg.types[string(typ)] = struct{}{}
				}
				if skipOthers && string(typ) != filter.Type {
					if seq < ceiling {
						*floor = min(*floor, seq)
						seg.lines++
					}
					line = nil // counted, and it cannot match
				}
			}
		}
		if len(line) > 0 {
			var e Event
			decoded := json.Unmarshal(trimLine(line), &e) == nil
			if decoded {
				seg.types[e.Type] = struct{}{}
			}
			if decoded && e.Seq < ceiling {
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
			seg.complete = true
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
