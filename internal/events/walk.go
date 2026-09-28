package events

import (
	"fmt"
	"os"
	"path/filepath"
)

// walkBatch is how many events WalkWithInFlight hands its callback at once.
const walkBatch = 256

// WalkProvider is an optional extension for providers that can stream their
// history. Prefer it to List or ListInFlight whenever the caller folds, counts,
// or keeps only a few of the events it reads: a walk holds one small batch,
// while a List holds every match at once.
type WalkProvider interface {
	WalkInFlight(filter Filter, fn func(batch []Event) bool) error
}

// WalkWithInFlight hands every event that matches filter to fn, in seq order,
// a batch of at most walkBatch events at a time. It reads the same history as
// ReadFilteredWithInFlight: the .gz archives, any in-flight rotating-* file,
// and the active file.
//
// Use it instead of ReadFilteredWithInFlight whenever you fold or count events
// rather than keep them. ReadFilteredWithInFlight returns one slice of every
// match, and with an empty filter that is the whole history. On the town Mac on
// 2026-09-28 the history was 7.9 GB of JSON in 42 files, and one such read held
// the gc supervisor at about 14 GB for the rest of its life (hq-k9wi6n).
//
// fn owns each batch: the walk never reuses one. fn returns false to stop.
// Each seq is delivered at most once, so a .gz archive and the rotating file it
// was made from never double up. Filter.Limit is refused; stop from fn instead.
func WalkWithInFlight(path string, filter Filter, fn func(batch []Event) bool) error {
	if filter.Limit > 0 {
		return fmt.Errorf("events: WalkWithInFlight does not take Filter.Limit; return false from fn to stop")
	}

	// Open the active file BEFORE listing the archives. If a rotation lands in
	// between, it renames the file we already hold, and the listing then shows
	// that same segment as a rotating-* file or its .gz. The seq guard drops the
	// second copy. Listing first would let that rotation hide the segment from
	// both reads.
	active, activeSize, err := openActiveForWalk(path)
	if err != nil {
		return err
	}
	if active != nil {
		defer active.Close() //nolint:errcheck // read-only file
	}

	sources, err := listBackfillSources(filepath.Dir(path), filter.AfterSeq)
	if err != nil {
		return fmt.Errorf("listing event archives: %w", err)
	}
	maxSeq := filter.AfterSeq
	for _, src := range sources {
		// Sources are sorted by first seq, so every later one is out of range too.
		if filter.BeforeSeq > 0 && src.firstSeq >= filter.BeforeSeq {
			break
		}
		reader, err := openSegmentReader(src)
		if err != nil {
			return fmt.Errorf("reading %q: %w", filepath.Base(src.path), err)
		}
		if reader == nil {
			continue
		}
		more, err := walkSegment(reader, filter, &maxSeq, fn)
		reader.close()
		if err != nil {
			return fmt.Errorf("reading %q: %w", filepath.Base(src.path), err)
		}
		if !more {
			return nil
		}
	}

	if active == nil {
		return nil
	}
	reader, err := activeSegmentReader(active, activeSize)
	if err != nil {
		return fmt.Errorf("reading events: %w", err)
	}
	if _, err := walkSegment(reader, filter, &maxSeq, fn); err != nil {
		return fmt.Errorf("reading events: %w", err)
	}
	return nil
}

// openActiveForWalk opens the active log and reads its size. A missing file is
// not an error: the history may live only in archives, or not exist yet.
func openActiveForWalk(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("reading events: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("reading events: %w", err)
	}
	return f, info.Size(), nil
}

// walkSegment feeds one segment to fn in batches. It reports false when fn
// asked to stop. On a read error it still hands fn the events read before it.
func walkSegment(r *segmentReader, filter Filter, maxSeq *uint64, fn func([]Event) bool) (bool, error) {
	for {
		batch := make([]Event, 0, walkBatch)
		done, err := r.readInto(filter, maxSeq, &batch, walkBatch)
		if len(batch) > 0 && !fn(batch) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}
	}
}
