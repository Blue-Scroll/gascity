package runproj

import (
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

func TestCountCanonicalRunStatusesCoversEveryLifecycleState(t *testing.T) {
	root := func(id, status, outcome string) beads.Bead {
		metadata := beads.StringMap{
			beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2,
			beadmeta.KindMetadataKey:            beadmeta.KindRun,
			beadmeta.FormulaMetadataKey:         "test-formula",
		}
		if outcome != "" {
			metadata[beadmeta.OutcomeMetadataKey] = outcome
		}
		return beads.Bead{ID: id, Title: id, Type: "molecule", Status: status, Metadata: metadata}
	}
	child := func(id, rootID, status string) beads.Bead {
		return beads.Bead{
			ID: id, Title: id, Type: "task", Status: status,
			Metadata: beads.StringMap{beadmeta.RootBeadIDMetadataKey: rootID},
		}
	}

	pending := root("run-pending", "open", "")
	active := root("run-active", "open", "")
	waiting := root("run-waiting", "blocked", "")
	canceling := root("run-canceling", "open", "")
	canceling.Metadata[beadmeta.CancelRequestedMetadataKey] = "true"
	completed := root("run-completed", "closed", beadmeta.OutcomePass)
	failed := root("run-failed", "closed", beadmeta.OutcomeFail)
	canceled := root("run-canceled", "closed", beadmeta.OutcomeCanceled)
	skipped := root("run-skipped", "closed", beadmeta.OutcomeSkipped)

	beadList := []beads.Bead{
		pending,
		active,
		child("run-active.step", active.ID, "in_progress"),
		waiting,
		canceling,
		child("run-canceling.step", canceling.ID, "in_progress"),
		completed,
		failed,
		canceled,
		skipped,
	}
	_, lanes := BuildRunSummaryWithAllLanes(beadList)

	got := CountCanonicalRunStatuses(beadList, lanes)
	want := CanonicalRunStatusCounts{
		Pending: 1, Active: 1, Waiting: 1, Canceling: 1,
		Completed: 1, Failed: 1, Canceled: 1, Skipped: 1,
	}
	if got != want {
		t.Fatalf("CountCanonicalRunStatuses() = %+v, want %+v", got, want)
	}
}

func TestCanonicalRunStatusUsesClosedRootBeforeLanePhase(t *testing.T) {
	root := beads.Bead{
		ID: "run-failed", Status: "closed",
		Metadata: beads.StringMap{beadmeta.OutcomeMetadataKey: beadmeta.OutcomeFail},
	}

	got := CanonicalRunStatusForLane(RunLane{Phase: "active"}, &root, 3)
	if got != CanonicalRunStatusFailed {
		t.Fatalf("CanonicalRunStatusForLane() = %q, want %q", got, CanonicalRunStatusFailed)
	}
}

func TestCountCanonicalRunStatusesIncludesHistoryBeyondSummaryCap(t *testing.T) {
	const completedRuns = 55
	beadList := make([]beads.Bead, 0, completedRuns)
	for i := 0; i < completedRuns; i++ {
		beadList = append(beadList, beads.Bead{
			ID:     fmt.Sprintf("run-%02d", i),
			Title:  "completed run",
			Type:   "molecule",
			Status: "closed",
			Metadata: beads.StringMap{
				beadmeta.FormulaContractMetadataKey: beadmeta.FormulaContractGraphV2,
				beadmeta.KindMetadataKey:            beadmeta.KindRun,
				beadmeta.FormulaMetadataKey:         "test-formula",
				beadmeta.OutcomeMetadataKey:         beadmeta.OutcomePass,
			},
		})
	}
	summary, lanes := BuildRunSummaryWithAllLanes(beadList)
	if len(summary.HistoricalLanes) >= completedRuns {
		t.Fatalf("fixture did not cross the summary cap: historical=%d", len(summary.HistoricalLanes))
	}

	got := CountCanonicalRunStatuses(beadList, lanes)
	if got.Completed != completedRuns {
		t.Fatalf("completed = %d, want %d beyond summary cap", got.Completed, completedRuns)
	}
}

// TestCountCanonicalRunStatusesLaterDuplicateWins pins the root lookup when a
// bead ID appears twice: the later copy decides the status. The lookup indexes
// beadList by pointer instead of copying each bead (vn-cq6w8n8), and must keep
// the rule the copying map had.
func TestCountCanonicalRunStatusesLaterDuplicateWins(t *testing.T) {
	lane := RunLane{ID: "run-dup"}
	open := beads.Bead{ID: "run-dup", Status: "open"}
	closed := beads.Bead{ID: "run-dup", Status: "closed", Metadata: beads.StringMap{beadmeta.OutcomeMetadataKey: beadmeta.OutcomeFail}}

	if got := CountCanonicalRunStatuses([]beads.Bead{open, closed}, []RunLane{lane}); got.Failed != 1 {
		t.Fatalf("open then closed: counts = %+v, want Failed=1", got)
	}
	if got := CountCanonicalRunStatuses([]beads.Bead{closed, open}, []RunLane{lane}); got.Pending != 1 {
		t.Fatalf("closed then open: counts = %+v, want Pending=1", got)
	}
}
