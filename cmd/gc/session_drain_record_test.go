package main

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/runtime"
	sessions "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

// The drain record exists so a stopped session can say why, long after the
// controller that stopped it is gone. hq-qufuy — "the reconciler retires a slot
// out from under a working polecat" — sat open for a month because nothing on
// the bead could tell a healthy finish from a wrongful stop. These tests pin
// the three facts that answer it.

func drainRecordFixture(t *testing.T) (*sessions.Store, sessions.Info, runtime.Provider, *drainTracker, *clock.Fake) {
	t.Helper()
	now := time.Date(2026, 9, 22, 6, 13, 39, 0, time.UTC)
	clk := &clock.Fake{Time: now}
	sp := runtime.NewFake()
	dt := newDrainTracker()
	_ = sp.Start(context.Background(), "vessel-network--forge", runtime.Config{})

	store, _ := sessiontest.Store(t, makeWakeBead("s-forge", map[string]string{
		"session_name": "vessel-network--forge",
		"generation":   "3",
	}))
	info, err := store.Get("s-forge")
	if err != nil {
		t.Fatalf("Get after seed: %v", err)
	}
	return store, info, sp, dt, clk
}

func readDrainRecord(t *testing.T, store *sessions.Store, id string) sessions.Info {
	t.Helper()
	info, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get(%q): %v", id, err)
	}
	return info
}

func TestBeginSessionDrain_WritesTheReasonOntoTheBead(t *testing.T) {
	store, info, sp, dt, clk := drainRecordFixture(t)

	if !beginSessionDrainInfo(info, sp, dt, "no-wake-reason", clk, defaultDrainTimeout, store) {
		t.Fatal("beginSessionDrainInfo = false, want true")
	}

	got := readDrainRecord(t, store, "s-forge")
	if got.DrainReasonMetadata != "no-wake-reason" {
		t.Errorf("drain_reason = %q, want no-wake-reason", got.DrainReasonMetadata)
	}
	if got.DrainInitiatorMetadata != sessions.DrainInitiatorReconciler {
		t.Errorf("drain_initiator = %q, want %q", got.DrainInitiatorMetadata, sessions.DrainInitiatorReconciler)
	}
	if want := clk.Now().UTC().Format(time.RFC3339); got.DrainRequestedAtMetadata != want {
		t.Errorf("drain_requested_at = %q, want %q", got.DrainRequestedAtMetadata, want)
	}
	if got.DrainCanceledAtMetadata != "" {
		t.Errorf("drain_canceled_at = %q on a fresh drain, want empty", got.DrainCanceledAtMetadata)
	}
}

// A nil front door is the one thing this write must never turn into a failed
// drain: the record is evidence, and evidence never gates a decision the
// controller has already made.
func TestBeginSessionDrain_NilFrontDoorStillDrains(t *testing.T) {
	_, info, sp, dt, clk := drainRecordFixture(t)

	if !beginSessionDrainInfo(info, sp, dt, "idle", clk, defaultDrainTimeout, nil) {
		t.Fatal("beginSessionDrainInfo with a nil front door = false, want true")
	}
	if ds := dt.get("s-forge"); ds == nil || ds.reason != "idle" {
		t.Fatalf("drain tracker state = %+v, want reason idle", ds)
	}
}

// The take-back is the controller admitting it asked a working agent to stop.
// It is the measurement hq-qufuy needs, so it has to survive on the bead.
func TestCancelDrainForAssignedWork_RecordsTheTakeBack(t *testing.T) {
	store, info, sp, dt, clk := drainRecordFixture(t)

	if !beginSessionDrainInfo(info, sp, dt, "no-wake-reason", clk, defaultDrainTimeout, store) {
		t.Fatal("beginSessionDrainInfo = false, want true")
	}
	info = readDrainRecord(t, store, "s-forge")

	clk.Time = clk.Time.Add(100 * time.Second)
	if !cancelSessionDrainForAssignedWorkInfo(info, sp, dt, store, clk) {
		t.Fatal("cancelSessionDrainForAssignedWorkInfo = false, want true")
	}

	got := readDrainRecord(t, store, "s-forge")
	if want := clk.Now().UTC().Format(time.RFC3339); got.DrainCanceledAtMetadata != want {
		t.Errorf("drain_canceled_at = %q, want %q", got.DrainCanceledAtMetadata, want)
	}
	if got.DrainCancelCountMetadata != "1" {
		t.Errorf("drain_cancel_count = %q, want 1", got.DrainCancelCountMetadata)
	}
	if got.DrainReasonMetadata != "no-wake-reason" {
		t.Errorf("drain_reason = %q after a take-back, want the reason kept", got.DrainReasonMetadata)
	}
}

// Two take-backs on one session in one awake interval is the oscillation
// itself. The tally must count them, not overwrite.
func TestCancelDrainForAssignedWork_CountsRepeatTakeBacks(t *testing.T) {
	store, info, sp, dt, clk := drainRecordFixture(t)

	for round := 1; round <= 2; round++ {
		if !beginSessionDrainInfo(info, sp, dt, "no-wake-reason", clk, defaultDrainTimeout, store) {
			t.Fatalf("round %d: beginSessionDrainInfo = false, want true", round)
		}
		info = readDrainRecord(t, store, "s-forge")
		clk.Time = clk.Time.Add(time.Minute)
		if !cancelSessionDrainForAssignedWorkInfo(info, sp, dt, store, clk) {
			t.Fatalf("round %d: cancel = false, want true", round)
		}
		info = readDrainRecord(t, store, "s-forge")
	}

	if got := readDrainRecord(t, store, "s-forge"); got.DrainCancelCountMetadata != "2" {
		t.Errorf("drain_cancel_count after two take-backs = %q, want 2", got.DrainCancelCountMetadata)
	}
}

// A second drain is not the first one's take-back. Clearing drain_canceled_at
// keeps a later reader from pairing this drain with an older cancel.
func TestBeginSessionDrain_ClearsAStaleTakeBackStamp(t *testing.T) {
	store, info, sp, dt, clk := drainRecordFixture(t)

	if !beginSessionDrainInfo(info, sp, dt, "no-wake-reason", clk, defaultDrainTimeout, store) {
		t.Fatal("first beginSessionDrainInfo = false, want true")
	}
	info = readDrainRecord(t, store, "s-forge")
	if !cancelSessionDrainForAssignedWorkInfo(info, sp, dt, store, clk) {
		t.Fatal("cancel = false, want true")
	}
	info = readDrainRecord(t, store, "s-forge")

	clk.Time = clk.Time.Add(5 * time.Minute)
	if !beginSessionDrainInfo(info, sp, dt, "idle", clk, defaultDrainTimeout, store) {
		t.Fatal("second beginSessionDrainInfo = false, want true")
	}

	got := readDrainRecord(t, store, "s-forge")
	if got.DrainCanceledAtMetadata != "" {
		t.Errorf("drain_canceled_at = %q on a new drain, want empty", got.DrainCanceledAtMetadata)
	}
	if got.DrainCancelCountMetadata != "1" {
		t.Errorf("drain_cancel_count = %q, want the interval tally kept at 1", got.DrainCancelCountMetadata)
	}
	if got.DrainReasonMetadata != "idle" {
		t.Errorf("drain_reason = %q, want idle", got.DrainReasonMetadata)
	}
}
