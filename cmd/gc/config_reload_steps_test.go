package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
)

// The bug this guards (vn-m2mso5v): a patrol tick spent 2h28m inside
// reloadConfigTraced and nothing named the step. Now a step that runs long is
// named in the supervisor log WHILE it is still running, and the reload's
// trace record carries every step's time. The slow step here is the beads
// lifecycle; the stub only returns once the warning has been written, so the
// test fails if the warning comes late or never.
func TestConfigReloadNamesASlowStepWhileItRunsAndTracesEveryStep(t *testing.T) {
	prevSlow, prevEvery := reloadStepSlowAfter, reloadStepWatchEvery
	reloadStepSlowAfter, reloadStepWatchEvery = 40*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { reloadStepSlowAfter, reloadStepWatchEvery = prevSlow, prevEvery })

	cityPath := t.TempDir()
	tomlPath := filepath.Join(cityPath, "city.toml")
	writeCityRuntimeConfig(t, tomlPath, "fake")
	cfg, revision := loadCityRuntimeControllerConfig(t, cityPath)
	provider := runtime.NewFake()
	stderr := &lockedBuffer{}
	cr := newTestCityRuntime(t, CityRuntimeParams{
		CityPath:  cityPath,
		CityName:  "test-city",
		TomlPath:  tomlPath,
		ConfigRev: revision,
		Cfg:       cfg,
		SP:        provider,
		BuildFn: func(*config.City, runtime.Provider, beads.Store) DesiredStateResult {
			return DesiredStateResult{State: map[string]TemplateParams{}}
		},
		Dops:   newDrainOps(provider),
		Rec:    events.Discard,
		Stdout: io.Discard,
		Stderr: stderr,
	})
	cs := newControllerState(context.Background(), cfg, provider, events.NewFake(), "test-city", cityPath)
	cs.cityBeadStore = beads.NewMemStore()
	cr.setControllerState(cs)

	const warning = "config reload: step beads_lifecycle has run"
	warnedWhileRunning := false
	previousLifecycle := cityRuntimeStartBeadsLifecycle
	cityRuntimeStartBeadsLifecycle = func(string, string, *config.City, io.Writer) error {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(stderr.String(), warning) {
				warnedWhileRunning = true
				return nil
			}
			time.Sleep(5 * time.Millisecond)
		}
		return nil
	}
	t.Cleanup(func() { cityRuntimeStartBeadsLifecycle = previousLifecycle })

	// A changed config takes the applied path, which runs every step.
	if err := os.WriteFile(tomlPath, []byte("[workspace]\nname = \"test-city\"\n\n[beads]\nprovider = \"file\"\n\n[session]\nprovider = \"fake\"\n\n[daemon]\nshutdown_timeout = \"1s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cr.trace = newSessionReconcilerTraceManager(cityPath, "test-city", io.Discard)
	cycle := cr.trace.beginCycle(sessionReconcilerTraceCycleInfo{TickTrigger: "patrol", CityPath: cityPath}, cr.cfg, nil)
	lastProviderName := "fake"
	reply := cr.reloadConfigTraced(context.Background(), &lastProviderName, cityPath, cycle, reloadSourceWatch)
	cycle.end(TraceCompletionCompleted, traceRecordPayload{"phase": "tick"})

	if reply.Outcome != reloadOutcomeApplied {
		t.Fatalf("reply = %+v, want applied; stderr=%q", reply, stderr.String())
	}
	if !warnedWhileRunning {
		t.Fatalf("no %q line while the step ran; stderr=%q", warning, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "config reload took") || !strings.Contains(got, "slowest steps: beads_lifecycle") {
		t.Fatalf("stderr has no summary naming beads_lifecycle as slowest: %q", got)
	}

	var fields map[string]any
	for _, r := range closeTrace(t, cr) {
		if r.RecordType == TraceRecordOperation && r.SiteCode == TraceSiteConfigReload && r.Fields["operation_name"] == "config_reload.steps" {
			fields = r.Fields
		}
	}
	if fields == nil {
		t.Fatal("the reload wrote no config_reload.steps operation record")
	}
	for _, step := range []string{
		"load_candidate", "prepare", "validate_rigs", "beads_lifecycle", "resolve_formulas",
		"provider_swap", "publish_runtime_config", "runtime_trackers", "order_dispatcher",
		"service_reload", "standalone_stores", "convergence", "config_watcher", "skill_materialization",
	} {
		for _, suffix := range []string{"_ms", "_max_ms", "_bd_calls"} {
			if _, ok := fields[step+suffix]; !ok {
				t.Errorf("config_reload.steps lacks %s%s: %#v", step, suffix, fields)
			}
		}
	}
	if ms, err := strconv.ParseFloat(fmt.Sprint(fields["beads_lifecycle_ms"]), 64); err != nil || ms < 40 {
		t.Errorf("beads_lifecycle_ms = %v, want the stub's wait (at least 40)", fields["beads_lifecycle_ms"])
	}
}

// A stall of hours must add a handful of lines, not one per watch: the
// warning repeats only when the step's run time doubles.
func TestReloadStepWarningRepeatsOnlyWhenTheRunTimeDoubles(t *testing.T) {
	prevSlow := reloadStepSlowAfter
	reloadStepSlowAfter = 2 * time.Minute
	t.Cleanup(func() { reloadStepSlowAfter = prevSlow })

	stderr := &lockedBuffer{}
	s := &reloadSteps{stderr: stderr, logPrefix: "gc test"}
	s.enter("beads_lifecycle")
	began := s.since
	for _, after := range []time.Duration{
		time.Minute, 3 * time.Minute, 3*time.Minute + 30*time.Second, 4 * time.Minute,
		7 * time.Minute, 8 * time.Minute, 2*time.Hour + 28*time.Minute,
	} {
		s.warnIfSlow(began.Add(after))
	}

	got := strings.Count(stderr.String(), "step beads_lifecycle has run")
	if got != 4 { // at 3m, 4m, 8m and 2h28m
		t.Fatalf("warnings = %d, want 4; stderr=%q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "has run 2h28m0s and has not returned") {
		t.Fatalf("last warning does not give the run time: %q", stderr.String())
	}

	// A new step starts its own clock and its own threshold.
	s.enter("resolve_formulas")
	s.warnIfSlow(s.since.Add(time.Minute))
	if strings.Contains(stderr.String(), "resolve_formulas") {
		t.Fatalf("a 1m step warned: %q", stderr.String())
	}
}
