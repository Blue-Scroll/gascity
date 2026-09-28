package runproj

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// Projector folds bead lifecycle events into the latest snapshot per id while
// preserving first-seen (creation) order. BuildRunSummary groups by first-seen
// order (mirroring the JS Map insertion order of the dashboard's listBeads
// read), so a plain Fold map — whose Go iteration order is random — would make a
// live run view flicker between requests. The per-city tailer drives a Projector
// instead: a cold ColdLoad over the full log, then incremental Apply of newly
// tailed events, and Beads() hands BuildRunSummary a deterministic slice.
//
// A Projector is not safe for concurrent use; the tailer mutates it from its
// single loop goroutine and publishes the built summary under its own lock.
type Projector struct {
	beads        map[string]beads.Bead
	order        []string
	lastSeq      uint64
	decodeMisses int
}

// RunProjectionSnapshot is one immutable bead snapshot published by an
// incremental run projector. Ready distinguishes a genuinely empty city from a
// cold replay that has not completed. Beads and their nested values are
// immutable after publication, so concurrent readers may share the slice.
type RunProjectionSnapshot struct {
	Ready        bool
	Beads        []beads.Bead
	DecodeMisses int
	Partial      bool
}

// NewProjector returns an empty projector.
func NewProjector() *Projector {
	return &Projector{beads: make(map[string]beads.Bead)}
}

// ColdLoad folds the entire event log at path into the projector. It walks
// the rotated .gz archives AND any in-flight events.jsonl.rotating-* file the
// recorder has not yet gzipped, so a cold start inside a rotation's compression
// window still sees those events. Safe to call once on a fresh projector before
// the incremental tail begins.
//
// It folds one batch at a time and never holds the whole log. Reading the log
// into one slice first (events.ReadFilteredWithInFlight) held the gc supervisor
// at about 14 GB, because the town's history is 7.9 GB of JSON (hq-k9wi6n). On a
// read error the batches before it stay folded and the error is returned, so
// the caller can mark the projection partial.
func (p *Projector) ColdLoad(path string) error {
	return events.WalkWithInFlight(path, events.Filter{}, func(batch []events.Event) bool {
		p.Apply(batch)
		return true
	})
}

// Apply folds a chronological event slice, upserting bead.created/updated/closed
// snapshots and removing bead.deleted ones, preserving first-seen order for new
// ids. It advances the cursor past every event (bead or not) and reports whether
// any bead snapshot changed, so the caller can skip a rebuild on a no-op tick.
// A bead.* event whose payload does not decode is counted (DecodeMisses) rather
// than swallowed, so a silent projection starve is observable to the caller.
func (p *Projector) Apply(evts []events.Event) (changed bool) {
	for i := range evts {
		e := &evts[i]
		if e.Seq > p.lastSeq {
			p.lastSeq = e.Seq
		}
		if !beadEventTypes[e.Type] {
			continue
		}
		b, ok := decodeBead(*e)
		if !ok {
			p.decodeMisses++
			continue
		}
		if e.Type == events.BeadDeleted {
			if _, exists := p.beads[b.ID]; exists {
				delete(p.beads, b.ID)
				p.removeOrder(b.ID)
				changed = true
			}
			continue
		}
		if _, exists := p.beads[b.ID]; !exists {
			p.order = append(p.order, b.ID)
		}
		p.beads[b.ID] = b
		changed = true
	}
	return changed
}

// Beads returns the folded beads in first-seen order — the deterministic input
// BuildRunSummary expects.
func (p *Projector) Beads() []beads.Bead {
	out := make([]beads.Bead, 0, len(p.order))
	for _, id := range p.order {
		if b, ok := p.beads[id]; ok {
			out = append(out, b)
		}
	}
	return out
}

// RunBeads returns the beads that take part in run classification
// (RunBeadFilter), in first-seen order. It is FilterRunBeads(p.Beads()) in one
// pass. Use it on any hot path: the projector holds every bead the log has ever
// named (422,941 on the town Mac on 2026-09-28), and the run tailer rebuilds on
// every poll that changes a bead, so the two-pass form copied that whole set
// twice a second (hq-k9wi6n).
func (p *Projector) RunBeads() []beads.Bead {
	out := make([]beads.Bead, 0, len(p.order))
	for _, id := range p.order {
		if b, ok := p.beads[id]; ok && RunBeadFilter(b) {
			out = append(out, b)
		}
	}
	return out
}

// LastSeq returns the highest event seq applied — the cursor a live tail resumes
// from.
func (p *Projector) LastSeq() uint64 { return p.lastSeq }

// DecodeMisses returns the cumulative count of bead.* events whose payload did
// not decode to a bead with an id. It is monotonic across Apply calls; the
// tailer watches the delta so a live projection starve (the run-view RCA
// signature) surfaces as a log line instead of a blank view.
func (p *Projector) DecodeMisses() int { return p.decodeMisses }

func (p *Projector) removeOrder(id string) {
	for i, oid := range p.order {
		if oid == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			return
		}
	}
}
