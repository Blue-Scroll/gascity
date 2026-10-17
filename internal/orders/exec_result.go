package orders

import (
	"fmt"
	"strconv"
	"time"
)

// Metadata keys an exec order run's tracking bead carries once its command
// has ended. They are the only record of HOW a run ended: the outcome label
// says exec or exec-failed, and without these a run the controller killed at
// its timeout reads exactly like one that exited 1.
const (
	// MetaExecDurationMs is the wall time of the command, in milliseconds.
	// Its presence is what marks a run as having a recorded result.
	MetaExecDurationMs = "order.exec_duration_ms"
	// MetaExecTimeoutMs is the limit the command ran under, in milliseconds.
	MetaExecTimeoutMs = "order.exec_timeout_ms"
	// MetaExecExitCode is the process exit status. Absent when the process
	// did not exit on its own (killed at its timeout, canceled, never started).
	MetaExecExitCode = "order.exec_exit_code"
	// MetaExecTimedOut is "true" when the controller killed the command at
	// its timeout, else "false".
	MetaExecTimedOut = "order.exec_timed_out"
)

// ExecResult is how one exec order run's command ended. It is not the
// ExecOutcome a script declares about skipped work: that is what the command
// says, this is what the controller saw.
type ExecResult struct {
	// Duration is the wall time from starting the command to its end.
	Duration time.Duration
	// Timeout is the limit the command ran under.
	Timeout time.Duration
	// ExitCode is the exit status of a process that exited on its own. Nil
	// when it did not: killed at its timeout, canceled, or never started.
	ExitCode *int
	// TimedOut reports that the controller killed the command at Timeout.
	TimedOut bool
}

// KilledReason is the one line that tells a person a run was killed at its
// timeout. Every reader that reports a timed-out run says it this way, so the
// words match across the order.failed event, `gc order run` and the API.
func (r ExecResult) KilledReason() string {
	return fmt.Sprintf("killed at its %s timeout", r.Timeout)
}

// Metadata encodes the result as tracking-bead metadata. A timed-out run
// never carries an exit code, even if one was set: the status of a killed
// process is the kill, not an answer from the command.
func (r ExecResult) Metadata() map[string]string {
	meta := map[string]string{
		MetaExecDurationMs: strconv.FormatInt(r.Duration.Milliseconds(), 10),
		MetaExecTimeoutMs:  strconv.FormatInt(r.Timeout.Milliseconds(), 10),
		MetaExecTimedOut:   strconv.FormatBool(r.TimedOut),
	}
	if r.ExitCode != nil && !r.TimedOut {
		meta[MetaExecExitCode] = strconv.Itoa(*r.ExitCode)
	}
	return meta
}

// ExecResultFromMetadata decodes a tracking bead's exec result. ok is false
// when the bead has none: a wisp run, an in-flight run, a run whose command
// never started, or a bead written before results were recorded.
func ExecResultFromMetadata(meta map[string]string) (ExecResult, bool) {
	durationMs, err := strconv.ParseInt(meta[MetaExecDurationMs], 10, 64)
	if err != nil {
		return ExecResult{}, false
	}
	r := ExecResult{
		Duration: time.Duration(durationMs) * time.Millisecond,
		TimedOut: meta[MetaExecTimedOut] == "true",
	}
	if timeoutMs, err := strconv.ParseInt(meta[MetaExecTimeoutMs], 10, 64); err == nil {
		r.Timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	if code, err := strconv.Atoi(meta[MetaExecExitCode]); err == nil {
		r.ExitCode = &code
	}
	return r, true
}
