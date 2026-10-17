package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/orders"
)

// execRunOf returns the one decoded run of an order, failing the test unless
// it recorded an exec result.
func execRunOf(t *testing.T, store beads.Store, scoped string) orders.OrderRun {
	t.Helper()
	runs, err := orders.NewStore(beads.OrdersStore{Store: store}).RecentRuns(scoped, 10)
	if err != nil {
		t.Fatalf("RecentRuns(%q): %v", scoped, err)
	}
	if len(runs) != 1 {
		t.Fatalf("RecentRuns(%q) = %d runs, want 1", scoped, len(runs))
	}
	if runs[0].Exec == nil {
		t.Fatalf("run %s of %q recorded no exec result", runs[0].ID, scoped)
	}
	return runs[0]
}

func recordedOfType(r *memRecorder, typ string) []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []events.Event
	for _, e := range r.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// A run the controller killed at its timeout must say so on its tracking bead
// and in a typed event, not only as a generic order.failed (vn-fe6fsvx).
func TestOrderDispatchExecTimeoutIsRecordedAndLoud(t *testing.T) {
	store := beads.NewMemStore()
	var rec memRecorder
	blockUntilKilled := func(ctx context.Context, _, _ string, _ []string) ([]byte, error) {
		<-ctx.Done()
		return []byte("swept rig one\n"), ctx.Err()
	}
	aa := []orders.Order{{
		Name:     "slow-sweep",
		Trigger:  "cooldown",
		Interval: "1m",
		Exec:     "scripts/sweep.sh",
		Timeout:  "100ms",
	}}
	ad := buildOrderDispatcherFromListExec(aa, store, nil, blockUntilKilled, &rec)

	ad.dispatch(context.Background(), t.TempDir(), time.Now())
	ad.drain(context.Background())

	run := execRunOf(t, store, "slow-sweep")
	if run.Outcome != orders.RunOutcomeExecFailed {
		t.Fatalf("outcome = %v, want RunOutcomeExecFailed", run.Outcome)
	}
	if !run.Exec.TimedOut || run.Exec.ExitCode != nil || run.Exec.Timeout != 100*time.Millisecond {
		t.Fatalf("exec result = %+v, want timed out at 100ms with no exit code", *run.Exec)
	}
	// The timeout clock starts before the env is built, so under load the
	// command can get less than the limit, or none of it (0ms). It never gets
	// more.
	if run.Exec.Duration < 0 || run.Exec.Duration > 100*time.Millisecond+5*time.Second {
		t.Fatalf("duration = %s, want the time the command ran before the kill", run.Exec.Duration)
	}

	failed := recordedOfType(&rec, events.OrderFailed)
	if len(failed) != 1 || !strings.HasPrefix(failed[0].Message, "killed at its 100ms timeout") {
		t.Fatalf("order.failed = %+v, want one saying it was killed at its timeout", failed)
	}
	if !strings.Contains(failed[0].Message, "swept rig one") {
		t.Fatalf("order.failed message %q dropped the command's own output", failed[0].Message)
	}
	timedOut := recordedOfType(&rec, events.OrderTimedOut)
	if len(timedOut) != 1 {
		t.Fatalf("order.timed_out events = %d, want 1", len(timedOut))
	}
	var payload events.OrderTimedOutPayload
	if err := json.Unmarshal(timedOut[0].Payload, &payload); err != nil {
		t.Fatalf("order.timed_out payload: %v", err)
	}
	if payload.OrderName != "slow-sweep" || payload.BeadID != run.ID || payload.TimeoutMS != 100 || payload.DurationMS != run.Exec.Duration.Milliseconds() {
		t.Fatalf("order.timed_out payload = %+v, want slow-sweep on %s, 100ms limit", payload, run.ID)
	}
}

// A command that exits on its own keeps its real exit status, and nothing
// calls it a timeout.
func TestOrderDispatchExecRecordsExitStatus(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		wantCode int
		outcome  orders.RunOutcome
	}{
		{"exit 0", "true", 0, orders.RunOutcomeExec},
		{"exit 3", "exit 3", 3, orders.RunOutcomeExecFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := beads.NewMemStore()
			var rec memRecorder
			aa := []orders.Order{{
				Name:     "real-exec",
				Trigger:  "cooldown",
				Interval: "1m",
				Exec:     tc.command,
				Timeout:  "30s",
			}}
			mad := buildOrderDispatcherFromListExec(aa, store, nil, shellExecRunner, &rec).(*memoryOrderDispatcher)
			mad.stderr = &bytes.Buffer{}
			tracking, err := orders.NewStore(beads.OrdersStore{Store: store}).CreateRun("real-exec", orders.RunOpts{})
			if err != nil {
				t.Fatal(err)
			}

			mad.dispatchExec(context.Background(), orders.NewStore(beads.OrdersStore{Store: store}), execStoreTarget{ScopeRoot: t.TempDir()}, aa[0], t.TempDir(), tracking.ID, nil)

			run := execRunOf(t, store, "real-exec")
			if run.Outcome != tc.outcome {
				t.Fatalf("outcome = %v, want %v", run.Outcome, tc.outcome)
			}
			if run.Exec.ExitCode == nil || *run.Exec.ExitCode != tc.wantCode {
				t.Fatalf("exit code = %v, want %d", run.Exec.ExitCode, tc.wantCode)
			}
			if run.Exec.TimedOut || run.Exec.Timeout != 30*time.Second {
				t.Fatalf("exec result = %+v, want not timed out under a 30s limit", *run.Exec)
			}
			if rec.hasType(events.OrderTimedOut) {
				t.Fatal("a command that exited on its own must not emit order.timed_out")
			}
		})
	}
}

func TestOrderExecResultClassifiesHowTheCommandEnded(t *testing.T) {
	const timeout = 5 * time.Second
	if r := orderExecResult(nil, time.Second, timeout); r.ExitCode == nil || *r.ExitCode != 0 || r.TimedOut {
		t.Fatalf("nil error = %+v, want exit 0", r)
	}
	// shellExecRunner joins a cleanup error onto the deadline; still a timeout.
	joined := errors.Join(context.DeadlineExceeded, fmt.Errorf("process group cleanup failed"))
	if r := orderExecResult(joined, timeout, timeout); !r.TimedOut || r.ExitCode != nil {
		t.Fatalf("joined deadline = %+v, want timed out with no exit code", r)
	}
	// The controller shutting down is not the order's timeout.
	if r := orderExecResult(context.Canceled, time.Second, timeout); r.TimedOut || r.ExitCode != nil {
		t.Fatalf("canceled = %+v, want neither a timeout nor an exit code", r)
	}
	if r := orderExecResult(errors.New("exit status 1"), time.Second, timeout); r.TimedOut || r.ExitCode != nil {
		t.Fatalf("untyped error = %+v, want no invented exit code", r)
	}
}

// `gc order history --json` shows how each exec run ended, read from what the
// bead store hands back for a tracking bead.
func TestOrderHistoryJSONShowsExecResult(t *testing.T) {
	store := beads.NewBdStore(t.TempDir(), func(_, _ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "--label=order-run:sweep") {
			return []byte(`[
{"id":"WP-2","title":"order:sweep","status":"closed","issue_type":"task","created_at":"2026-10-16T23:36:00Z","labels":["order-run:sweep","exec-failed"],
 "metadata":{"order.exec_duration_ms":"900004","order.exec_timeout_ms":"900000","order.exec_timed_out":"true"}},
{"id":"WP-1","title":"order:sweep","status":"closed","issue_type":"task","created_at":"2026-10-16T23:21:00Z","labels":["order-run:sweep","exec"],
 "metadata":{"order.exec_duration_ms":"571000","order.exec_timeout_ms":"900000","order.exec_timed_out":"false","order.exec_exit_code":"0"}},
{"id":"WP-0","title":"order:sweep","status":"closed","issue_type":"task","created_at":"2026-10-16T23:06:00Z","labels":["order-run:sweep","exec"]}
]`), nil
		}
		return []byte(`[]`), nil
	})
	aa := []orders.Order{{Name: "sweep", Exec: "scripts/sweep.sh"}}
	resolver := func(orders.Order) ([]beads.OrdersStore, error) {
		return []beads.OrdersStore{{Store: store}}, nil
	}

	var stdout, stderr bytes.Buffer
	if code := doOrderHistoryWithStoresResolverJSON("sweep", "", aa, resolver, true, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr.String())
	}
	var payload struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if len(payload.Entries) != 3 {
		t.Fatalf("entries = %d, want 3: %s", len(payload.Entries), stdout.String())
	}
	killed, clean, legacy := payload.Entries[0], payload.Entries[1], payload.Entries[2]
	if killed["timed_out"] != true || killed["duration_ms"] != float64(900004) || killed["timeout_ms"] != float64(900000) {
		t.Fatalf("killed run = %v, want timed_out with its duration and limit", killed)
	}
	if _, ok := killed["exit_code"]; ok {
		t.Fatalf("killed run = %v, must carry no exit_code", killed)
	}
	if clean["timed_out"] != false || clean["exit_code"] != float64(0) || clean["duration_ms"] != float64(571000) {
		t.Fatalf("clean run = %v, want exit 0, not timed out, 571000ms", clean)
	}
	for _, key := range []string{"timed_out", "exit_code", "duration_ms", "timeout_ms"} {
		if _, ok := legacy[key]; ok {
			t.Fatalf("a run with no recorded result = %v, must not carry %q", legacy, key)
		}
	}
}
