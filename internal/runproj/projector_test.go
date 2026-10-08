package runproj

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

func TestProjectorPreservesFirstSeenOrder(t *testing.T) {
	p := NewProjector()
	changed := p.Apply([]events.Event{
		beadEvent(1, events.BeadCreated, "b", "open"),
		beadEvent(2, events.BeadCreated, "a", "open"),
		beadEvent(3, events.BeadCreated, "c", "open"),
		// An update to an existing bead must NOT reorder it.
		beadEvent(4, events.BeadUpdated, "b", "in_progress"),
	})
	if !changed {
		t.Fatal("Apply reported no change for bead events")
	}

	got := idsOf(p.Beads())
	want := []string{"b", "a", "c"}
	if !equalIDs(got, want) {
		t.Errorf("order = %v, want %v (first-seen order, stable across updates)", got, want)
	}
	if p.LastSeq() != 4 {
		t.Errorf("lastSeq = %d, want 4", p.LastSeq())
	}
}

func TestProjectorIncrementalApplyAndDelete(t *testing.T) {
	p := NewProjector()
	p.Apply([]events.Event{
		beadEvent(1, events.BeadCreated, "a", "open"),
		beadEvent(2, events.BeadCreated, "b", "open"),
	})

	// Incremental tail: a new bead appends at the end; a delete drops its slot.
	changed := p.Apply([]events.Event{
		beadEvent(3, events.BeadCreated, "c", "open"),
		beadEvent(4, events.BeadDeleted, "a", "open"),
	})
	if !changed {
		t.Fatal("Apply reported no change")
	}

	got := idsOf(p.Beads())
	want := []string{"b", "c"}
	if !equalIDs(got, want) {
		t.Errorf("order after delete = %v, want %v", got, want)
	}
	if p.LastSeq() != 4 {
		t.Errorf("lastSeq = %d, want 4", p.LastSeq())
	}
}

func TestProjectorNoOpTickReportsUnchanged(t *testing.T) {
	p := NewProjector()
	p.Apply([]events.Event{beadEvent(1, events.BeadCreated, "a", "open")})

	// A tick carrying only non-bead events advances the cursor but changes no
	// bead, so the tailer can skip the rebuild.
	changed := p.Apply([]events.Event{{Seq: 7, Type: events.SessionWoke, Subject: "w"}})
	if changed {
		t.Error("non-bead event should not report a change")
	}
	if p.LastSeq() != 7 {
		t.Errorf("lastSeq = %d, want 7 (cursor advances past ignored events)", p.LastSeq())
	}
}

// TestProjectorCountsDecodeMisses proves the tailer-driven path (Projector.Apply,
// not the free Apply) counts bead.* payload decode misses instead of swallowing
// them — the observable signal the run tailer logs so a live projection starve
// surfaces. Non-bead events are not misses, and the count is cumulative.
func TestProjectorCountsDecodeMisses(t *testing.T) {
	p := NewProjector()
	p.Apply([]events.Event{
		beadEvent(1, events.BeadCreated, "a", "open"),
		{Seq: 2, Type: events.BeadUpdated, Payload: json.RawMessage(`{"status":"open"}`)}, // no id → miss
		{Seq: 3, Type: events.SessionWoke, Subject: "w"},                                  // not a bead event
	})
	if p.DecodeMisses() != 1 {
		t.Fatalf("DecodeMisses = %d, want 1 after first pass", p.DecodeMisses())
	}
	// A second pass with another undecodable bead.* event accumulates the count.
	p.Apply([]events.Event{{Seq: 4, Type: events.BeadClosed, Payload: json.RawMessage(`garbage`)}})
	if p.DecodeMisses() != 2 {
		t.Errorf("DecodeMisses = %d, want 2 (cumulative across passes)", p.DecodeMisses())
	}
}

func idsOf(bl []beads.Bead) []string {
	out := make([]string, len(bl))
	for i, b := range bl {
		out[i] = b.ID
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestProjectorRunBeadsMatchesTheTwoPassFilter(t *testing.T) {
	message, _ := json.Marshal(beads.Bead{ID: "m", Status: "open", Type: "message"})
	control, _ := json.Marshal(beads.Bead{ID: "g", Status: "open", Type: "task", Labels: []string{"gc:control"}})
	p := NewProjector()
	p.Apply([]events.Event{
		beadEvent(1, events.BeadCreated, "b", "open"),
		{Seq: 2, Type: events.BeadCreated, Payload: message},
		beadEvent(3, events.BeadCreated, "a", "open"),
		{Seq: 4, Type: events.BeadCreated, Payload: control},
	})

	got := idsOf(p.RunBeads())
	if want := idsOf(FilterRunBeads(p.Beads())); !equalIDs(got, want) {
		t.Fatalf("RunBeads = %v, want the two-pass filter's %v", got, want)
	}
	if !equalIDs(got, []string{"b", "a"}) {
		t.Fatalf("RunBeads = %v, want [b a]: the message and gc: beads are not run beads", got)
	}
}

// TestProjectorColdLoadFoldsAcrossWalkBatches proves the batched cold load
// folds exactly like one Apply over the whole log: an update that lands in a
// later batch still wins, and first-seen order holds across the seam.
func TestProjectorColdLoadFoldsAcrossWalkBatches(t *testing.T) {
	const creates = 600 // more than two walk batches
	var log []events.Event
	for i := 1; i <= creates; i++ {
		log = append(log, beadEvent(uint64(i), events.BeadCreated, fmt.Sprintf("b%03d", i), "open"))
	}
	log = append(log,
		beadEvent(creates+1, events.BeadUpdated, "b001", "closed"),
		beadEvent(creates+2, events.BeadDeleted, "b002", "open"),
	)
	path := filepath.Join(t.TempDir(), "events.jsonl")
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, e := range log {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	cold := NewProjector()
	if err := cold.ColdLoad(path); err != nil {
		t.Fatalf("ColdLoad: %v", err)
	}
	whole := NewProjector()
	whole.Apply(log)

	if got, want := idsOf(cold.Beads()), idsOf(whole.Beads()); !equalIDs(got, want) {
		t.Fatalf("cold-load order differs from one whole Apply (%d vs %d ids)", len(got), len(want))
	}
	if cold.LastSeq() != creates+2 {
		t.Fatalf("lastSeq = %d, want %d", cold.LastSeq(), creates+2)
	}
	if got := cold.Beads()[0]; got.ID != "b001" || got.Status != "closed" {
		t.Fatalf("first bead = %s/%s, want b001/closed from the later batch", got.ID, got.Status)
	}
}
