package session

import (
	"strings"
	"testing"
	"time"
)

func TestBeginDrainRecordPatch_SaysWhyAndWho(t *testing.T) {
	now := time.Date(2026, 9, 22, 6, 13, 39, 0, time.UTC)
	patch := BeginDrainRecordPatch(now, "no-wake-reason", DrainInitiatorReconciler)

	if got := patch[DrainReasonMetadataKey]; got != "no-wake-reason" {
		t.Errorf("drain_reason = %q, want no-wake-reason", got)
	}
	if got := patch[DrainInitiatorMetadataKey]; got != DrainInitiatorReconciler {
		t.Errorf("drain_initiator = %q, want %q", got, DrainInitiatorReconciler)
	}
	if got, want := patch[DrainRequestedAtMetadataKey], now.Format(time.RFC3339); got != want {
		t.Errorf("drain_requested_at = %q, want %q", got, want)
	}
	// A take-back belongs to the drain it took back. Carrying an older stamp
	// onto a new drain would pair a cancel with the wrong request.
	if got, ok := patch[DrainCanceledAtMetadataKey]; !ok || got != "" {
		t.Errorf("drain_canceled_at = %q (present=%v), want an explicit clear", got, ok)
	}
	// The tally spans the awake interval, so a new drain must not reset it.
	if _, ok := patch[DrainCancelCountMetadataKey]; ok {
		t.Error("drain_cancel_count was touched by a drain begin; it is an interval tally")
	}
}

func TestDrainCanceledPatch_CountsUp(t *testing.T) {
	now := time.Date(2026, 9, 22, 6, 15, 19, 0, time.UTC)

	for _, tt := range []struct {
		name  string
		prior int
		want  string
	}{
		{"first take-back", 0, "1"},
		{"second take-back", 1, "2"},
		{"unreadable tally floors at zero", -4, "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			patch := DrainCanceledPatch(now, tt.prior)
			if got := patch[DrainCancelCountMetadataKey]; got != tt.want {
				t.Errorf("drain_cancel_count = %q, want %q", got, tt.want)
			}
			if got, want := patch[DrainCanceledAtMetadataKey], now.Format(time.RFC3339); got != want {
				t.Errorf("drain_canceled_at = %q, want %q", got, want)
			}
		})
	}
}

// A pool slot is reused, so the next incarnation must not inherit the last
// one's drain story and be read as having been asked to stop.
func TestCommitStartedPatch_ClearsTheDrainRecordOnANewAwakeInterval(t *testing.T) {
	now := time.Date(2026, 9, 22, 7, 37, 51, 0, time.UTC)

	fresh := CommitStartedPatch(CommitStartedPatchInput{Now: now, StartsAwakeInterval: true})
	for _, key := range []string{
		DrainReasonMetadataKey,
		DrainInitiatorMetadataKey,
		DrainRequestedAtMetadataKey,
		DrainCanceledAtMetadataKey,
		DrainCancelCountMetadataKey,
	} {
		got, ok := fresh[key]
		if !ok || got != "" {
			t.Errorf("%s = %q (present=%v) on a new awake interval, want an explicit clear", key, got, ok)
		}
	}

	// A recovery re-confirmation of an already-running runtime is the SAME
	// interval, so its drain record has to survive.
	resumed := CommitStartedPatch(CommitStartedPatchInput{Now: now})
	if _, ok := resumed[DrainReasonMetadataKey]; ok {
		t.Error("drain_reason was cleared by a re-confirmation; that is the same awake interval")
	}
}

// The old wording named a cause it cannot know. Both a polecat that finished
// its work and a slot the controller retired out from under a working agent
// close with this one code, and the old sentence claimed the second for both.
// That wrong sentence was quoted as evidence in hq-qufuy.
func TestCanonicalCloseReason_DrainedDoesNotGuessWhy(t *testing.T) {
	got := CanonicalCloseReason("drained")

	if strings.Contains(got, "retired by reconciler") {
		t.Errorf("close reason = %q, want no claim about who stopped it", got)
	}
	if len(got) < 20 {
		t.Errorf("close reason = %q is %d chars; validation.on-close=error rejects under 20", got, len(got))
	}
}
