package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// The three tests here pin the fix for hq-ga1qga: the supervisor's nudge
// maintenance sweep cost 19s to 280s per tick on a queue that held nothing but
// dead-letter rows, because it opened a fresh store on every tick and re-asked
// that store, forever, about shadow beads that no longer existed.

// A dead row whose shadow bead is gone (wisp GC purged it) can never be
// confirmed terminal, so it used to be retained forever and re-queried on
// every pass. Now it is pruned once past retention, like a confirmed one; a
// young row is still kept.
func TestPruneDeadQueuedNudgesPrunesRowWhoseShadowBeadIsGone(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()
	store := openNudgeBeadStore(dir)
	if store.Store == nil {
		t.Fatal("openNudgeBeadStore returned nil")
	}
	now := time.Now().UTC()

	gone := func(id string, deadAt time.Time) queuedNudge {
		item := newQueuedNudgeWithOptions("worker", "shadow bead purged", "session", deadAt.Add(-time.Minute), queuedNudgeOptions{ID: id})
		item.BeadID = "gc-purged-" + id // no bead with this id exists
		item.LastError = "expired"
		item.DeadAt = deadAt
		return item
	}
	state := &nudgeQueueState{Dead: []queuedNudge{
		gone("n-old", now.Add(-3*time.Hour)),
		gone("n-young", now.Add(-10*time.Minute)),
	}}

	if err := pruneDeadQueuedNudges(state, nudgeFrontDoor(store), now, noMaintenanceDeadline()); err != nil {
		t.Fatalf("pruneDeadQueuedNudges: %v", err)
	}
	if len(state.Dead) != 1 || state.Dead[0].ID != "n-young" {
		t.Fatalf("dead = %v, want only n-young (a gone shadow bead is pruned once past retention)", queuedNudgeIDs(state.Dead))
	}

	// The repair attempt on a gone bead must not create a bead of its own.
	items, err := store.List(beads.ListQuery{Label: nudgeBeadLabel, IncludeClosed: true, Limit: 10})
	if err != nil {
		t.Fatalf("List nudge beads: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("nudge beads = %d, want 0 (pruning a gone shadow must not mint one)", len(items))
	}
}

// A dead row with no BeadID never reaches the store, so a queue holding only
// such rows must not open one. Twelve of them sat in the town's queue for
// weeks and opened Dolt on every supervisor tick.
func TestNudgeMaintenanceSkipsStoreOpenForDeadRowsWithoutBead(t *testing.T) {
	now := time.Now()
	seedDead := func(t *testing.T, dir string, beadID string) {
		t.Helper()
		err := withNudgeQueueState(dir, func(state *nudgeQueueState) error {
			state.Dead = append(state.Dead, queuedNudge{
				ID:      "n-dead-" + beadID,
				Agent:   "worker",
				Source:  "session",
				Message: "dead letter",
				BeadID:  beadID,
				// Young enough to survive every helper's retention prune, so
				// each helper still sees the same row.
				DeadAt: now.Add(-10 * time.Minute),
			})
			return nil
		})
		if err != nil {
			t.Fatalf("seed dead item: %v", err)
		}
	}
	runHelpers := func(t *testing.T, dir string) {
		t.Helper()
		if _, err := claimDueQueuedNudgesMatching(dir, now, func(queuedNudge) bool { return true }); err != nil {
			t.Fatalf("claimDueQueuedNudgesMatching: %v", err)
		}
		if _, _, _, err := listQueuedNudges(dir, "worker", now); err != nil {
			t.Fatalf("listQueuedNudges: %v", err)
		}
		if _, _, _, err := listQueuedNudgesForTarget(dir, nudgeTarget{cityPath: dir}, now); err != nil {
			t.Fatalf("listQueuedNudgesForTarget: %v", err)
		}
		if err := runNudgeQueueMaintenanceSweep(dir, nil, nil, now); err != nil {
			t.Fatalf("runNudgeQueueMaintenanceSweep: %v", err)
		}
	}

	t.Run("no bead id: never opens", func(t *testing.T) {
		opens, closes := installCountingNudgeStoreSeam(t)
		dir := t.TempDir()
		seedDead(t, dir, "")
		seedDead(t, dir, "")
		runHelpers(t, dir)
		if *opens != 0 || *closes != 0 {
			t.Fatalf("opens=%d closes=%d, want 0/0 (a dead row with no bead has no store work)", *opens, *closes)
		}
	})
	t.Run("bead id: opens once per helper", func(t *testing.T) {
		opens, closes := installCountingNudgeStoreSeam(t)
		dir := t.TempDir()
		seedDead(t, dir, "gc-dead-shadow")
		runHelpers(t, dir)
		// Four helpers ran, each owning one open and one close.
		if *opens != 4 || *closes != 4 {
			t.Fatalf("opens=%d closes=%d, want 4/4 (a dead row with a bead still needs the store)", *opens, *closes)
		}
	})
}

// The supervisor holds its nudges store open for the life of the process. The
// sweep borrows it: no second open (which would load the whole city config and
// dial the sql-server again) and no close of a handle it does not own. A nil
// store keeps the lazy open-and-close for callers with no handle.
func TestRunNudgeQueueMaintenanceSweepBorrowsTheCallerStore(t *testing.T) {
	now := time.Now()
	seedWork := func(t *testing.T, dir string, store beads.NudgesStore) {
		t.Helper()
		item := newQueuedNudgeWithOptions("worker", "do work", "session", now, queuedNudgeOptions{ID: "n-work"})
		if err := enqueueQueuedNudgeWithStore(dir, store, item); err != nil {
			t.Fatalf("enqueueQueuedNudgeWithStore: %v", err)
		}
	}

	t.Run("borrowed store is neither opened nor closed", func(t *testing.T) {
		opens, seamCloses := installCountingNudgeStoreSeam(t)
		dir := t.TempDir()
		callerCloses := 0
		caller := beads.NudgesStore{Store: &countingNudgeStore{MemStore: beads.NewMemStore(), closes: &callerCloses}}
		seedWork(t, dir, caller)
		if err := runNudgeQueueMaintenanceSweep(dir, caller.Store, caller.Store, now); err != nil {
			t.Fatalf("runNudgeQueueMaintenanceSweep: %v", err)
		}
		if *opens != 0 || *seamCloses != 0 {
			t.Fatalf("seam opens=%d closes=%d, want 0/0 (the sweep must reuse the caller's store)", *opens, *seamCloses)
		}
		if callerCloses != 0 {
			t.Fatalf("caller store closed %d times, want 0 (the caller still uses it)", callerCloses)
		}
	})
	t.Run("nil store falls back to one open and one close", func(t *testing.T) {
		opens, closes := installCountingNudgeStoreSeam(t)
		dir := t.TempDir()
		seedWork(t, dir, openNudgeBeadStore(dir))
		opensBefore, closesBefore := *opens, *closes
		if err := runNudgeQueueMaintenanceSweep(dir, nil, nil, now); err != nil {
			t.Fatalf("runNudgeQueueMaintenanceSweep: %v", err)
		}
		if got := *opens - opensBefore; got != 1 {
			t.Fatalf("opens delta=%d, want 1", got)
		}
		if got := *closes - closesBefore; got != 1 {
			t.Fatalf("closes delta=%d, want 1", got)
		}
	})
}
