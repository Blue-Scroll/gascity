package main

import (
	"context"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// TestReconcileSessionBeads_AliveFreshModeReassignCyclesConversation verifies
// the fix for gastownhall/gascity#1893: an alive on_demand named session
// running wake_mode=fresh must cycle its conversation when bd update points
// the assignee at a new bead. The session keeps the same bead identifier in
// the store (it's a named session) but its conversation lineage is reset so
// the next wake starts fresh on the newly assigned bead.
func TestReconcileSessionBeads_AliveFreshModeReassignCyclesConversation(t *testing.T) {
	env := newRestartRequestTestEnv()
	env.cfg = &config.City{
		Workspace:     config.Workspace{Name: "test-city"},
		Agents:        []config.Agent{{Name: "witness", StartCommand: "true", MaxActiveSessions: restartRequestTestIntPtr(1)}},
		NamedSessions: []config.NamedSession{{Template: "witness", Mode: "on_demand"}},
	}
	sessionName := config.NamedSessionRuntimeName(env.cfg.Workspace.Name, env.cfg.Workspace, "witness")
	env.desiredState[sessionName] = TemplateParams{
		Command:      "true",
		SessionName:  sessionName,
		TemplateName: "witness",
		ResolvedProvider: &config.ResolvedProvider{
			SessionIDFlag: "--session-id",
		},
	}

	// wb-A is the bead the witness was on. It is closed now, which is the
	// proof the cycle needs that the witness really left it.
	prev := putReassignWorkBead(t, env, "closed", "witness")

	session := env.createSessionBead(sessionName)
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "witness",
		namedSessionModeMetadata:     "on_demand",
		"template":                   "witness",
		"state":                      "active",
		"wake_mode":                  "fresh",
		"session_key":                "conversation-A",
		sessionpkg.CurrentBeadIDKey:  prev.ID,
	})
	if err := env.sp.Start(context.Background(), sessionName, runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := env.sp.SetMeta(sessionName, "GC_SESSION_ID", session.ID); err != nil {
		t.Fatalf("SetMeta(GC_SESSION_ID): %v", err)
	}

	// Patrol formula poured wb-B and pointed the witness's assignee at it;
	// wb-A is closed, so the reconciler only sees wb-B.
	workBead := beads.Bead{ID: "wb-B", Title: "next witness wisp", Type: "task", Status: "in_progress", Assignee: "witness"}

	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, []beads.Bead{workBead})

	if env.sp.IsRunning(sessionName) {
		t.Fatal("session should have been killed by fresh-cycle")
	}
	got, _ := env.store.Get(session.ID)
	if got.Metadata[sessionpkg.CurrentBeadIDKey] != "wb-B" {
		t.Fatalf("%s = %q, want wb-B", sessionpkg.CurrentBeadIDKey, got.Metadata[sessionpkg.CurrentBeadIDKey])
	}
	if got.Metadata["started_config_hash"] != "" {
		t.Fatalf("started_config_hash = %q, want empty so the next wake takes the first-start path", got.Metadata["started_config_hash"])
	}
	if got.Metadata["continuation_reset_pending"] != "true" {
		t.Fatalf("continuation_reset_pending = %q, want true", got.Metadata["continuation_reset_pending"])
	}
	if got.Metadata["session_key"] == "" || got.Metadata["session_key"] == "conversation-A" {
		t.Fatalf("session_key = %q, want rotated key", got.Metadata["session_key"])
	}
}

// TestReconcileSessionBeads_AliveResumeModeReassignKeepsConversation verifies
// that wake_mode=resume sessions DO NOT cycle on bead reassign — the
// existing conversation is preserved and the agent picks up the new bead
// from its work query at its next prompt boundary.
func TestReconcileSessionBeads_AliveResumeModeReassignKeepsConversation(t *testing.T) {
	env := newRestartRequestTestEnv()
	env.cfg = &config.City{
		Workspace:     config.Workspace{Name: "test-city"},
		Agents:        []config.Agent{{Name: "witness", StartCommand: "true", MaxActiveSessions: restartRequestTestIntPtr(1)}},
		NamedSessions: []config.NamedSession{{Template: "witness", Mode: "on_demand"}},
	}
	sessionName := config.NamedSessionRuntimeName(env.cfg.Workspace.Name, env.cfg.Workspace, "witness")
	env.desiredState[sessionName] = TemplateParams{
		Command:      "true",
		SessionName:  sessionName,
		TemplateName: "witness",
		ResolvedProvider: &config.ResolvedProvider{
			SessionIDFlag: "--session-id",
		},
	}

	session := env.createSessionBead(sessionName)
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "witness",
		namedSessionModeMetadata:     "on_demand",
		"template":                   "witness",
		"state":                      "active",
		// wake_mode unset (default = resume)
		"session_key":               "conversation-A",
		sessionpkg.CurrentBeadIDKey: "wb-A",
	})
	if err := env.sp.Start(context.Background(), sessionName, runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := env.sp.SetMeta(sessionName, "GC_SESSION_ID", session.ID); err != nil {
		t.Fatalf("SetMeta(GC_SESSION_ID): %v", err)
	}

	workBead := beads.Bead{ID: "wb-B", Title: "next witness wisp", Type: "task", Status: "in_progress", Assignee: "witness"}

	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, []beads.Bead{workBead})

	if !env.sp.IsRunning(sessionName) {
		t.Fatal("resume-mode session should still be running — divergence must not cycle non-fresh sessions")
	}
	got, _ := env.store.Get(session.ID)
	if got.Metadata["session_key"] != "conversation-A" {
		t.Fatalf("session_key = %q, want conversation-A preserved", got.Metadata["session_key"])
	}
	if got.Metadata["continuation_reset_pending"] == "true" {
		t.Fatalf("continuation_reset_pending = true, want unset for resume mode (no cycle should have run)")
	}
}

// TestReconcileSessionBeads_AsleepWakeRecordsCurrentBead pins the recording
// half of the contract: when an asleep session is woken because of an
// assigned bead, the reconciler must stamp currently_processing_bead_id
// onto the session bead. Without this, the next reassign cycle would have
// no recorded current bead to compare against.
func TestReconcileSessionBeads_AsleepWakeRecordsCurrentBead(t *testing.T) {
	env := newRestartRequestTestEnv()
	env.cfg = &config.City{
		Workspace:     config.Workspace{Name: "test-city"},
		Agents:        []config.Agent{{Name: "witness", StartCommand: "true", MaxActiveSessions: restartRequestTestIntPtr(1)}},
		NamedSessions: []config.NamedSession{{Template: "witness", Mode: "on_demand"}},
	}
	sessionName := config.NamedSessionRuntimeName(env.cfg.Workspace.Name, env.cfg.Workspace, "witness")
	env.desiredState[sessionName] = TemplateParams{
		Command:      "true",
		SessionName:  sessionName,
		TemplateName: "witness",
		ResolvedProvider: &config.ResolvedProvider{
			SessionIDFlag: "--session-id",
		},
	}

	session := env.createSessionBead(sessionName)
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "witness",
		namedSessionModeMetadata:     "on_demand",
		"template":                   "witness",
		"state":                      "asleep",
		"wake_mode":                  "fresh",
	})

	workBead := beads.Bead{ID: "wb-77", Title: "witness wisp", Type: "task", Status: "in_progress", Assignee: "witness"}

	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, []beads.Bead{workBead})

	got, _ := env.store.Get(session.ID)
	if got.Metadata[sessionpkg.CurrentBeadIDKey] != "wb-77" {
		t.Fatalf("%s = %q, want wb-77 recorded at wake", sessionpkg.CurrentBeadIDKey, got.Metadata[sessionpkg.CurrentBeadIDKey])
	}
}

// TestReconcileSessionBeads_RecoveryPrefersRecordedBead pins crash-recovery
// behavior: when a session is asleep with a recorded current bead AND
// multiple beads are assigned, the reconciler must anchor on the recorded
// bead so the agent resumes the work it was last actively processing.
func TestReconcileSessionBeads_RecoveryPrefersRecordedBead(t *testing.T) {
	env := newRestartRequestTestEnv()
	env.cfg = &config.City{
		Workspace:     config.Workspace{Name: "test-city"},
		Agents:        []config.Agent{{Name: "witness", StartCommand: "true", MaxActiveSessions: restartRequestTestIntPtr(1)}},
		NamedSessions: []config.NamedSession{{Template: "witness", Mode: "on_demand"}},
	}
	sessionName := config.NamedSessionRuntimeName(env.cfg.Workspace.Name, env.cfg.Workspace, "witness")
	env.desiredState[sessionName] = TemplateParams{
		Command:      "true",
		SessionName:  sessionName,
		TemplateName: "witness",
		ResolvedProvider: &config.ResolvedProvider{
			SessionIDFlag: "--session-id",
		},
	}

	session := env.createSessionBead(sessionName)
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "witness",
		namedSessionModeMetadata:     "on_demand",
		"template":                   "witness",
		"state":                      "asleep",
		"wake_mode":                  "fresh",
		sessionpkg.CurrentBeadIDKey:  "wb-current",
	})

	other := beads.Bead{ID: "wb-other", Title: "other wisp", Type: "task", Status: "open", Assignee: "witness"}
	current := beads.Bead{ID: "wb-current", Title: "current wisp", Type: "task", Status: "in_progress", Assignee: "witness"}

	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, []beads.Bead{other, current})

	got, _ := env.store.Get(session.ID)
	if got.Metadata[sessionpkg.CurrentBeadIDKey] != "wb-current" {
		t.Fatalf("%s = %q, want wb-current preserved across restart", sessionpkg.CurrentBeadIDKey, got.Metadata[sessionpkg.CurrentBeadIDKey])
	}
}

// reconcileSessionBeadsWithAssignedWork is a test-only wrapper that mirrors
// restartRequestTestEnv.reconcile but threads assignedWorkBeads through so
// ComputeAwakeSet sees the work demand. Tests for assigned-work-driven
// behavior need this hook; the existing helper in
// session_reconciler_restart_request_test.go intentionally passes nil.
func reconcileSessionBeadsWithAssignedWork(env *restartRequestTestEnv, sessions []beads.Bead, assignedWork []beads.Bead, opts ...startExecutionOption) {
	poolDesired := make(map[string]int)
	for _, tp := range env.desiredState {
		if tp.TemplateName != "" {
			poolDesired[tp.TemplateName]++
		}
	}
	cfgNames := configuredSessionNames(env.cfg, "", env.store)
	_ = reconcileSessionBeads(
		context.Background(),
		sessions,
		env.desiredState,
		cfgNames,
		env.cfg,
		env.sp,
		env.store,
		nil,
		assignedWork,
		nil,
		env.dt,
		poolDesired,
		false,
		nil,
		"",
		nil,
		env.clk,
		env.rec,
		0,
		0,
		&env.stdout,
		&env.stderr,
		opts...,
	)
}

// putReassignWorkBead stores a work bead with the given status and assignee
// and returns it, so the fresh-cycle proof has a real bead to read.
func putReassignWorkBead(t *testing.T, env *restartRequestTestEnv, status, assignee string) beads.Bead {
	t.Helper()
	b, err := env.store.Create(beads.Bead{Title: "work", Type: "task"})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}
	if status == "closed" {
		if err := env.store.Update(b.ID, beads.UpdateOpts{Assignee: &assignee}); err != nil {
			t.Fatalf("assign work bead: %v", err)
		}
		if err := env.store.Close(b.ID); err != nil {
			t.Fatalf("close work bead: %v", err)
		}
	} else if err := env.store.Update(b.ID, beads.UpdateOpts{Status: &status, Assignee: &assignee}); err != nil {
		t.Fatalf("update work bead: %v", err)
	}
	got, err := env.store.Get(b.ID)
	if err != nil {
		t.Fatalf("read work bead: %v", err)
	}
	return got
}

// freshReassignRefinery builds an alive wake_mode=fresh named "refinery"
// session whose recorded bead is recorded. It returns the env, the session
// bead and its runtime name.
func freshReassignRefinery(t *testing.T, recorded func(env *restartRequestTestEnv) string) (*restartRequestTestEnv, beads.Bead, string) {
	t.Helper()
	env := newRestartRequestTestEnv()
	env.cfg = &config.City{
		Workspace:     config.Workspace{Name: "test-city"},
		Agents:        []config.Agent{{Name: "refinery", StartCommand: "true", MaxActiveSessions: restartRequestTestIntPtr(1)}},
		NamedSessions: []config.NamedSession{{Template: "refinery", Mode: "on_demand"}},
	}
	sessionName := config.NamedSessionRuntimeName(env.cfg.Workspace.Name, env.cfg.Workspace, "refinery")
	env.desiredState[sessionName] = TemplateParams{
		Command:      "true",
		SessionName:  sessionName,
		TemplateName: "refinery",
		ResolvedProvider: &config.ResolvedProvider{
			SessionIDFlag: "--session-id",
		},
	}
	recordedID := recorded(env)
	session := env.createSessionBead(sessionName)
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "refinery",
		namedSessionModeMetadata:     "on_demand",
		"template":                   "refinery",
		"state":                      "active",
		"wake_mode":                  "fresh",
		"session_key":                "conversation-A",
		sessionpkg.CurrentBeadIDKey:  recordedID,
	})
	if err := env.sp.Start(context.Background(), sessionName, runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := env.sp.SetMeta(sessionName, "GC_SESSION_ID", session.ID); err != nil {
		t.Fatalf("SetMeta(GC_SESSION_ID): %v", err)
	}
	return env, session, sessionName
}

func assertFreshSessionNotCycled(t *testing.T, env *restartRequestTestEnv, session beads.Bead, sessionName, wantRecorded string) {
	t.Helper()
	if !env.sp.IsRunning(sessionName) {
		t.Fatalf("live session was killed; stdout=%q stderr=%q", env.stdout.String(), env.stderr.String())
	}
	got, _ := env.store.Get(session.ID)
	if got.Metadata[sessionpkg.CurrentBeadIDKey] != wantRecorded {
		t.Fatalf("%s = %q, want %q kept: the session still holds it", sessionpkg.CurrentBeadIDKey, got.Metadata[sessionpkg.CurrentBeadIDKey], wantRecorded)
	}
	if got.Metadata["session_key"] != "conversation-A" {
		t.Fatalf("session_key = %q, want conversation-A kept", got.Metadata["session_key"])
	}
	if got.Metadata["continuation_reset_pending"] == "true" {
		t.Fatal("continuation_reset_pending = true, want unset: no cycle should have run")
	}
	if strings.Contains(env.stdout.String(), "Cycled fresh-mode session") {
		t.Fatalf("stdout reports a cycle: %q", env.stdout.String())
	}
}

// TestReconcileSessionBeads_FreshReassign_OwnBeadMissingFromOneListDoesNotCycle
// replays 2026-09-11 17:04:44 UTC (vn-9y7tkv1). The refinery is mid-turn on
// its in-progress wisp. One tick's work list does not contain that wisp, and
// the only bead it does show is another bead ALREADY assigned to the same
// refinery. The old code called that a reassignment and killed the session.
// The wisp is still in_progress and still the refinery's, so nothing moved.
func TestReconcileSessionBeads_FreshReassign_OwnBeadMissingFromOneListDoesNotCycle(t *testing.T) {
	var wisp beads.Bead
	env, session, sessionName := freshReassignRefinery(t, func(env *restartRequestTestEnv) string {
		wisp = putReassignWorkBead(t, env, "in_progress", "refinery")
		return wisp.ID
	})
	other := putReassignWorkBead(t, env, "open", "refinery")
	gate := newFreshReassignGate()
	list := []beads.Bead{{ID: other.ID, Title: "other", Type: "task", Status: "in_progress", Assignee: "refinery"}}

	// Two ticks with the same short list: still no cycle, and the refusal is
	// logged once, not once per tick.
	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, list, withFreshReassignGate(gate))
	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, list, withFreshReassignGate(gate))

	assertFreshSessionNotCycled(t, env, session, sessionName, wisp.ID)
	if n := strings.Count(env.stderr.String(), "not cycling fresh-mode session"); n != 1 {
		t.Fatalf("refusal logged %d times, want exactly 1; stderr=%q", n, env.stderr.String())
	}
	if !strings.Contains(env.stderr.String(), "still assigned to this session") {
		t.Fatalf("refusal does not say why; stderr=%q", env.stderr.String())
	}
}

// TestReconcileSessionBeads_FreshReassign_OpenNotReadyAnchorDoesNotCycle
// replays 17:07 the same day. After the first bad kill the record pointed at
// an open bead that was NOT ready, so the list never carried it, and the
// fallback picked the wisp the new session had just claimed. The open bead is
// still the refinery's, so this is not a reassignment either.
func TestReconcileSessionBeads_FreshReassign_OpenNotReadyAnchorDoesNotCycle(t *testing.T) {
	var anchor beads.Bead
	env, session, sessionName := freshReassignRefinery(t, func(env *restartRequestTestEnv) string {
		anchor = putReassignWorkBead(t, env, "open", "refinery")
		return anchor.ID
	})
	wisp := putReassignWorkBead(t, env, "in_progress", "refinery")
	list := []beads.Bead{{ID: wisp.ID, Title: "wisp", Type: "task", Status: "in_progress", Assignee: "refinery"}}

	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, list, withFreshReassignGate(newFreshReassignGate()))

	assertFreshSessionNotCycled(t, env, session, sessionName, anchor.ID)
}

// TestReconcileSessionBeads_FreshReassign_RecordedBeadAssignedElsewhereCycles
// is the real reassignment: the recorded bead now belongs to someone else.
// That is proof, so the cycle runs on the first tick.
func TestReconcileSessionBeads_FreshReassign_RecordedBeadAssignedElsewhereCycles(t *testing.T) {
	env, session, sessionName := freshReassignRefinery(t, func(env *restartRequestTestEnv) string {
		return putReassignWorkBead(t, env, "in_progress", "someone-else").ID
	})
	next := putReassignWorkBead(t, env, "in_progress", "refinery")
	list := []beads.Bead{{ID: next.ID, Title: "next", Type: "task", Status: "in_progress", Assignee: "refinery"}}

	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, list, withFreshReassignGate(newFreshReassignGate()))

	if env.sp.IsRunning(sessionName) {
		t.Fatalf("session should have been cycled; stderr=%q", env.stderr.String())
	}
	got, _ := env.store.Get(session.ID)
	if got.Metadata[sessionpkg.CurrentBeadIDKey] != next.ID {
		t.Fatalf("%s = %q, want %q", sessionpkg.CurrentBeadIDKey, got.Metadata[sessionpkg.CurrentBeadIDKey], next.ID)
	}
}

// TestReconcileSessionBeads_FreshReassign_BurnedBeadCyclesOnSecondMiss covers
// a burned wisp: it is deleted, not closed, so no store can find it. One miss
// is not proof (a failed wisp read also comes back "not found"). The same
// miss on the next tick is.
func TestReconcileSessionBeads_FreshReassign_BurnedBeadCyclesOnSecondMiss(t *testing.T) {
	env, session, sessionName := freshReassignRefinery(t, func(*restartRequestTestEnv) string {
		return "wb-burned"
	})
	next := putReassignWorkBead(t, env, "in_progress", "refinery")
	list := []beads.Bead{{ID: next.ID, Title: "next", Type: "task", Status: "in_progress", Assignee: "refinery"}}
	gate := newFreshReassignGate()

	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, list, withFreshReassignGate(gate))
	assertFreshSessionNotCycled(t, env, session, sessionName, "wb-burned")

	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, list, withFreshReassignGate(gate))
	if env.sp.IsRunning(sessionName) {
		t.Fatalf("second miss should have cycled the session; stderr=%q", env.stderr.String())
	}
}

// TestReconcileSessionBeads_FreshReassign_OneMissWithoutGateNeverCycles pins
// the nil-gate default: a caller that passes no gate can refuse a kill but can
// never cause one from a missing read.
func TestReconcileSessionBeads_FreshReassign_OneMissWithoutGateNeverCycles(t *testing.T) {
	env, session, sessionName := freshReassignRefinery(t, func(*restartRequestTestEnv) string {
		return "wb-burned"
	})
	next := putReassignWorkBead(t, env, "in_progress", "refinery")
	list := []beads.Bead{{ID: next.ID, Title: "next", Type: "task", Status: "in_progress", Assignee: "refinery"}}

	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, list)
	reconcileSessionBeadsWithAssignedWork(env, []beads.Bead{session}, list)

	assertFreshSessionNotCycled(t, env, session, sessionName, "wb-burned")
}
