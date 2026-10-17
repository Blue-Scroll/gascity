package orders

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

func intPtr(n int) *int { return &n }

func TestExecResultMetadataRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   ExecResult
	}{
		{"exit 0", ExecResult{Duration: 1500 * time.Millisecond, Timeout: 5 * time.Minute, ExitCode: intPtr(0)}},
		{"exit 3", ExecResult{Duration: 42 * time.Millisecond, Timeout: time.Minute, ExitCode: intPtr(3)}},
		{"killed at timeout", ExecResult{Duration: 900 * time.Second, Timeout: 900 * time.Second, TimedOut: true}},
		{"no exit code, no timeout", ExecResult{Duration: time.Second, Timeout: time.Minute}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ExecResultFromMetadata(tc.in.Metadata())
			if !ok {
				t.Fatalf("ExecResultFromMetadata(%v) reported no result", tc.in.Metadata())
			}
			if got.Duration != tc.in.Duration || got.Timeout != tc.in.Timeout || got.TimedOut != tc.in.TimedOut {
				t.Fatalf("round trip = %+v, want %+v", got, tc.in)
			}
			switch {
			case tc.in.ExitCode == nil && got.ExitCode != nil:
				t.Fatalf("exit code = %d, want none", *got.ExitCode)
			case tc.in.ExitCode != nil && (got.ExitCode == nil || *got.ExitCode != *tc.in.ExitCode):
				t.Fatalf("exit code = %v, want %d", got.ExitCode, *tc.in.ExitCode)
			}
		})
	}
}

func TestExecResultTimedOutWritesNoExitCode(t *testing.T) {
	meta := ExecResult{Duration: time.Second, Timeout: time.Second, TimedOut: true, ExitCode: intPtr(-1)}.Metadata()
	if _, ok := meta[MetaExecExitCode]; ok {
		t.Fatalf("a killed run must not record an exit code, got %q", meta[MetaExecExitCode])
	}
	if meta[MetaExecTimedOut] != "true" {
		t.Fatalf("%s = %q, want true", MetaExecTimedOut, meta[MetaExecTimedOut])
	}
}

func TestExecResultFromMetadataWithoutResult(t *testing.T) {
	if _, ok := ExecResultFromMetadata(nil); ok {
		t.Fatal("nil metadata must report no result")
	}
	if _, ok := ExecResultFromMetadata(map[string]string{MetaExecDurationMs: "not-a-number"}); ok {
		t.Fatal("an unreadable duration must report no result")
	}
}

func TestSetExecOutcomeStampsLabelAndResultInOneRun(t *testing.T) {
	mem := beads.NewMemStore()
	s := NewStore(beads.OrdersStore{Store: mem})
	run, err := s.CreateRun("sweep", RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	want := ExecResult{Duration: 900 * time.Second, Timeout: 900 * time.Second, TimedOut: true}
	if err := s.SetExecOutcome(run.ID, RunOutcomeExecFailed, &want); err != nil {
		t.Fatalf("SetExecOutcome: %v", err)
	}

	runs, err := s.RecentRuns("sweep", 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("RecentRuns returned %d runs, want 1", len(runs))
	}
	got := runs[0]
	if got.Outcome != RunOutcomeExecFailed {
		t.Fatalf("outcome = %v, want RunOutcomeExecFailed", got.Outcome)
	}
	if got.Exec == nil {
		t.Fatal("decoded run has no exec result")
	}
	if !got.Exec.TimedOut || got.Exec.Duration != want.Duration || got.Exec.ExitCode != nil {
		t.Fatalf("decoded exec result = %+v, want %+v", *got.Exec, want)
	}
}

func TestDecodeRunWithoutExecResult(t *testing.T) {
	mem := beads.NewMemStore()
	s := NewStore(beads.OrdersStore{Store: mem})
	run, err := s.CreateRun("digest", RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.SetOutcome(run.ID, RunOutcomeWisp); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}
	runs, err := s.RecentRuns("digest", 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Exec != nil {
		t.Fatalf("a run with no recorded exec result must decode Exec=nil, got %+v", runs)
	}
}

func TestExecResultKilledReasonNamesTheTimeout(t *testing.T) {
	got := ExecResult{Timeout: 15 * time.Minute, TimedOut: true}.KilledReason()
	if got != "killed at its 15m0s timeout" {
		t.Fatalf("KilledReason() = %q", got)
	}
}

func TestSetExecOutcomeWithoutResultWritesOnlyTheLabel(t *testing.T) {
	mem := beads.NewMemStore()
	s := NewStore(beads.OrdersStore{Store: mem})
	run, err := s.CreateRun("env-broken", RunOpts{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.SetExecOutcome(run.ID, RunOutcomeExecEnvFailed, nil); err != nil {
		t.Fatalf("SetExecOutcome: %v", err)
	}
	runs, err := s.RecentRuns("env-broken", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("RecentRuns = %v, %v; want one run", runs, err)
	}
	if runs[0].Outcome != RunOutcomeExecEnvFailed || runs[0].Exec != nil {
		t.Fatalf("run = %+v, want exec-env-failed with no exec result", runs[0])
	}
}
