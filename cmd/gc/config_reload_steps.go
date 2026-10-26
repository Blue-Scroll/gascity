package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// reloadStepSlowAfter is how long one config reload step may run before the
// supervisor log says so. The warning repeats each time the step's run time
// doubles (2m, 4m, 8m ...), so a stall of hours adds a handful of lines, not
// one a minute. A whole reload that ran this long also gets one summary line
// naming its slowest steps.
//
// It is a var so a test can shrink it.
var reloadStepSlowAfter = 2 * time.Minute

// reloadStepWatchEvery is how often the watcher looks at the open step.
var reloadStepWatchEvery = 15 * time.Second

// reloadSteps times each step of one config reload, so a slow reload names
// the step it was slow in.
//
// Why it exists: on 2026-10-26 one patrol tick spent 2h28m inside
// reloadConfigTraced before any traced phase ran, and nothing said where. The
// trace's config_reload record has no timings, the supervisor log went quiet
// between the reload's first warning and "Config reloaded", and every patrol
// tick waited behind it (vn-m2mso5v). Applied reloads routinely take 3 to 30
// minutes on the town, so the same question comes back on every slow tick.
//
// It answers in two places:
//   - the trace: one config_reload.steps operation record per reload, with
//     <step>_ms, <step>_max_ms and <step>_bd_calls for each step that ran.
//     That lands only when the reload returns.
//   - the supervisor log, WHILE the step is still running: "config reload:
//     step beads_lifecycle has run 4m0s and has not returned". So a stall
//     names itself before anyone restarts the supervisor, and the goroutine
//     dump (which stops the process) is needed only to see inside that step.
//
// Add a step with enter(name) where it begins: it closes the step that was
// open. A step with no enter of its own is charged to the step before it, so
// no time in the reload goes uncounted.
type reloadSteps struct {
	mu     sync.Mutex
	laps   tickPhaseLaps
	open   string
	since  time.Time
	warnAt time.Duration

	start     tickPhaseStart
	stderr    io.Writer
	logPrefix string
	stop      chan struct{}
	stopped   chan struct{}
}

// startReloadSteps starts the clock and the watcher for one reload. Call
// finish exactly once when the reload returns, on every path.
func startReloadSteps(stderr io.Writer, logPrefix string) *reloadSteps {
	s := &reloadSteps{
		start:     startTickPhase(),
		stderr:    stderr,
		logPrefix: logPrefix,
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	go s.watch()
	return s
}

// enter closes the open step and opens the step called name.
func (s *reloadSteps) enter(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.laps.enter(name)
	s.open = name
	s.since = time.Now()
	s.warnAt = reloadStepSlowAfter
}

// watch warns while one step runs long. It never touches the reload itself.
func (s *reloadSteps) watch() {
	defer close(s.stopped)
	ticker := time.NewTicker(reloadStepWatchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.warnIfSlow(time.Now())
		}
	}
}

func (s *reloadSteps) warnIfSlow(now time.Time) {
	s.mu.Lock()
	name, ran, warnAt := s.open, now.Sub(s.since), s.warnAt
	if name == "" || ran < warnAt {
		s.mu.Unlock()
		return
	}
	for s.warnAt <= ran {
		s.warnAt *= 2
	}
	s.mu.Unlock()
	fmt.Fprintf(s.stderr, "%s: config reload: step %s has run %s and has not returned\n", //nolint:errcheck // best-effort stderr
		s.logPrefix, name, ran.Round(time.Second))
}

// finish stops the watcher, records the steps on trace (nil is fine), and
// logs a summary when the whole reload ran past reloadStepSlowAfter.
func (s *reloadSteps) finish(trace *sessionReconcilerTraceCycle) {
	close(s.stop)
	<-s.stopped

	s.mu.Lock()
	fields := tickPhaseParts{}
	s.laps.addTo(fields)
	s.open = ""
	s.mu.Unlock()

	trace.RecordTickPhase(TraceSiteConfigReload, "config_reload.steps", s.start, fields)

	total := s.start.elapsed()
	if total < reloadStepSlowAfter {
		return
	}
	fmt.Fprintf(s.stderr, "%s: config reload took %s; slowest steps: %s\n", //nolint:errcheck // best-effort stderr
		s.logPrefix, total.Round(time.Second), slowestReloadSteps(fields, 3))
}

// slowestReloadSteps names the n steps with the most time in fields, slowest
// first, as "beads_lifecycle 2h1m0s, resolve_formulas 3m0s".
func slowestReloadSteps(fields tickPhaseParts, n int) string {
	type step struct {
		name string
		ms   int64
	}
	var steps []step
	for key, value := range fields {
		name, ok := strings.CutSuffix(key, "_ms")
		if !ok || strings.HasSuffix(name, "_max") {
			continue
		}
		ms, _ := value.(int64)
		steps = append(steps, step{name: name, ms: ms})
	}
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].ms != steps[j].ms {
			return steps[i].ms > steps[j].ms
		}
		return steps[i].name < steps[j].name
	})
	if len(steps) > n {
		steps = steps[:n]
	}
	parts := make([]string, 0, len(steps))
	for _, st := range steps {
		parts = append(parts, fmt.Sprintf("%s %s", st.name, (time.Duration(st.ms)*time.Millisecond).Round(time.Second)))
	}
	return strings.Join(parts, ", ")
}
