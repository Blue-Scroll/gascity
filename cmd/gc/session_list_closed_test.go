package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// seedSessionListStore stands up a file-backed city store the CLI list path can
// open, with the fake runtime provider so no tmux is touched.
func seedSessionListStore(t *testing.T) beads.Store {
	t.Helper()
	clearGCEnv(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := t.TempDir()
	t.Setenv("GC_CITY", cityDir)
	writeNamedSessionCityTOML(t, cityDir)

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt(%q): %v", cityDir, err)
	}
	return store
}

// createClosedSessionBead writes a session bead and closes it, which is the
// only shape a drain record is ever read back from: the record is written as
// the session stops, so every bead carrying one is closed by the time anybody
// asks.
func createClosedSessionBead(t *testing.T, store beads.Store, sessionName string, metadata map[string]string) string {
	t.Helper()
	md := map[string]string{
		"session_name": sessionName,
		"template":     "worker",
	}
	for k, v := range metadata {
		md[k] = v
	}
	b, err := store.Create(beads.Bead{
		Title:    sessionName,
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession},
		Metadata: md,
	})
	if err != nil {
		t.Fatalf("store.Create(%s): %v", sessionName, err)
	}
	if err := store.Update(b.ID, beads.UpdateOpts{Status: stringPtr("closed")}); err != nil {
		t.Fatalf("store.Update(%s, closed): %v", sessionName, err)
	}
	return b.ID
}

func createOpenSessionBead(t *testing.T, store beads.Store, sessionName string) string {
	t.Helper()
	b, err := store.Create(beads.Bead{
		Title:  sessionName,
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": sessionName,
			"template":     "worker",
			"state":        "asleep",
		},
	})
	if err != nil {
		t.Fatalf("store.Create(%s): %v", sessionName, err)
	}
	return b.ID
}

// TestSessionListStateClosedReturnsClosedSessions is the regression test for
// vn-erdzkzw. Before the fix --state closed filtered the open-only snapshot, so
// it answered 0 rows however many closed sessions the store held.
func TestSessionListStateClosedReturnsClosedSessions(t *testing.T) {
	store := seedSessionListStore(t)
	createOpenSessionBead(t, store, "live-session")
	createClosedSessionBead(t, store, "ended-session", nil)

	var stdout, stderr bytes.Buffer
	if code := cmdSessionList(sessionListRequest{State: "closed", JSON: true}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionList(--state closed --json) = %d, want 0; stderr=%s", code, stderr.String())
	}

	var got sessionListJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a session list object: %v; stdout=%q", err, stdout.String())
	}
	if row := sessionListJSONRowBySessionName(got.Sessions, "ended-session"); row == nil {
		t.Fatalf("--state closed dropped the closed session; stdout=%q", stdout.String())
	} else if !row.Closed {
		t.Fatalf("ended-session closed = false, want true; row=%#v", row)
	}
	if row := sessionListJSONRowBySessionName(got.Sessions, "live-session"); row != nil {
		t.Fatalf("--state closed returned an OPEN session; row=%#v", row)
	}
	if got.Summary.Closed != 1 {
		t.Fatalf("summary.closed = %d, want 1; stdout=%q", got.Summary.Closed, stdout.String())
	}
}

// TestSessionListStateAllReturnsBothStates pins that "all" really means all.
// It is the flag the mayor's published validity check used, and it returned
// only live sessions.
func TestSessionListStateAllReturnsBothStates(t *testing.T) {
	store := seedSessionListStore(t)
	createOpenSessionBead(t, store, "live-session")
	createClosedSessionBead(t, store, "ended-session", nil)

	var stdout, stderr bytes.Buffer
	if code := cmdSessionList(sessionListRequest{State: "all", JSON: true}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionList(--state all --json) = %d, want 0; stderr=%s", code, stderr.String())
	}
	var got sessionListJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a session list object: %v; stdout=%q", err, stdout.String())
	}
	for _, name := range []string{"live-session", "ended-session"} {
		if sessionListJSONRowBySessionName(got.Sessions, name) == nil {
			t.Fatalf("--state all missing %q; stdout=%q", name, stdout.String())
		}
	}
}

// TestSessionListDefaultStillExcludesClosed pins that the default listing did
// not change. Closed history is opt-in, so the everyday command stays short.
func TestSessionListDefaultStillExcludesClosed(t *testing.T) {
	store := seedSessionListStore(t)
	createOpenSessionBead(t, store, "live-session")
	createClosedSessionBead(t, store, "ended-session", nil)

	var stdout, stderr bytes.Buffer
	if code := cmdSessionList(sessionListRequest{JSON: true}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionList(--json) = %d, want 0; stderr=%s", code, stderr.String())
	}
	var got sessionListJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a session list object: %v; stdout=%q", err, stdout.String())
	}
	if row := sessionListJSONRowBySessionName(got.Sessions, "ended-session"); row != nil {
		t.Fatalf("default listing leaked a closed session; row=%#v", row)
	}
}

// TestSessionListClosedCarriesDrainRecord is the capability vn-erdzkzw asked
// for: one supported way to ask which drains the controller began, and which of
// them it took back.
func TestSessionListClosedCarriesDrainRecord(t *testing.T) {
	store := seedSessionListStore(t)
	createClosedSessionBead(t, store, "taken-back", map[string]string{
		session.DrainReasonMetadataKey:      "no-wake-reason",
		session.DrainInitiatorMetadataKey:   session.DrainInitiatorReconciler,
		session.DrainRequestedAtMetadataKey: "2026-09-22T14:10:00Z",
		session.DrainCanceledAtMetadataKey:  "2026-09-22T14:12:00Z",
		session.DrainCancelCountMetadataKey: "1",
	})
	createClosedSessionBead(t, store, "self-stopped", nil)

	var stdout, stderr bytes.Buffer
	if code := cmdSessionList(sessionListRequest{State: "closed", JSON: true}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionList(--state closed --json) = %d, want 0; stderr=%s", code, stderr.String())
	}
	var got sessionListJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a session list object: %v; stdout=%q", err, stdout.String())
	}

	row := sessionListJSONRowBySessionName(got.Sessions, "taken-back")
	if row == nil {
		t.Fatalf("missing taken-back row; stdout=%q", stdout.String())
	}
	for _, tc := range []struct{ field, got, want string }{
		{"drain_reason", row.DrainReason, "no-wake-reason"},
		{"drain_initiator", row.DrainInitiator, session.DrainInitiatorReconciler},
		{"drain_requested_at", row.DrainRequestedAt, "2026-09-22T14:10:00Z"},
		{"drain_canceled_at", row.DrainCanceledAt, "2026-09-22T14:12:00Z"},
		{"drain_cancel_count", row.DrainCancelCount, "1"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}

	// The jq shape the bead asks for has to select exactly the controller
	// drains, and no others.
	selected := 0
	for _, r := range got.Sessions {
		if r.DrainInitiator == session.DrainInitiatorReconciler {
			selected++
		}
	}
	if selected != 1 {
		t.Fatalf("rows with drain_initiator=reconciler = %d, want 1; stdout=%q", selected, stdout.String())
	}

	// A session with no drain record must carry no drain fields at all, so a
	// count of controller drains is never inflated by an empty record.
	self := sessionListJSONRowBySessionName(got.Sessions, "self-stopped")
	if self == nil {
		t.Fatalf("missing self-stopped row; stdout=%q", stdout.String())
	}
	if self.DrainInitiator != "" || self.DrainReason != "" || self.DrainCancelCount != "" {
		t.Fatalf("self-stopped carries a drain record it never had: %#v", self)
	}
}

// TestClosedSessionDrainReasonCell pins the REASON cell a human reads. It says
// only what the record proves: an empty initiator can be the agent stopping
// itself OR a bead older than the record, and those are not distinguishable.
func TestClosedSessionDrainReasonCell(t *testing.T) {
	for _, tc := range []struct {
		name string
		info session.Info
		want string
	}{
		{
			name: "no record at all",
			info: session.Info{},
			want: "-",
		},
		{
			name: "empty initiator is never guessed at",
			info: session.Info{DrainReasonMetadata: "no-wake-reason"},
			want: "-",
		},
		{
			name: "controller drain",
			info: session.Info{
				DrainInitiatorMetadata: session.DrainInitiatorReconciler,
				DrainReasonMetadata:    "no-wake-reason",
			},
			want: "drain=controller:no-wake-reason",
		},
		{
			name: "controller drain with no reason code",
			info: session.Info{DrainInitiatorMetadata: session.DrainInitiatorReconciler},
			want: "drain=controller",
		},
		{
			name: "take-backs are never hidden",
			info: session.Info{
				DrainInitiatorMetadata:   session.DrainInitiatorReconciler,
				DrainReasonMetadata:      "no-wake-reason",
				DrainCancelCountMetadata: "2",
			},
			want: "drain=controller:no-wake-reason,take-backs=2",
		},
		{
			name: "a zero tally is not a take-back",
			info: session.Info{
				DrainInitiatorMetadata:   session.DrainInitiatorReconciler,
				DrainCancelCountMetadata: "0",
			},
			want: "drain=controller",
		},
		{
			name: "an unparseable tally is dropped, not printed raw",
			info: session.Info{
				DrainInitiatorMetadata:   session.DrainInitiatorReconciler,
				DrainCancelCountMetadata: "not-a-number",
			},
			want: "drain=controller",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := closedSessionDrainReason(tc.info); got != tc.want {
				t.Fatalf("closedSessionDrainReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSessionListClosedHumanRowShowsDrain proves the REASON cell reaches the
// table a person actually reads, not just --json.
func TestSessionListClosedHumanRowShowsDrain(t *testing.T) {
	store := seedSessionListStore(t)
	createClosedSessionBead(t, store, "taken-back", map[string]string{
		session.DrainReasonMetadataKey:      "no-wake-reason",
		session.DrainInitiatorMetadataKey:   session.DrainInitiatorReconciler,
		session.DrainCancelCountMetadataKey: "1",
	})

	var stdout, stderr bytes.Buffer
	if code := cmdSessionList(sessionListRequest{State: "closed"}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionList(--state closed) = %d, want 0; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	row := findRowContaining(lines, "taken-back")
	if row == "" {
		t.Fatalf("no row for the closed session:\n%s", out)
	}
	if !strings.Contains(row, "drain=controller:no-wake-reason,take-backs=1") {
		t.Fatalf("REASON cell missing the drain record; row=%q\nfull output:\n%s", row, out)
	}
}

func TestSessionListRequestWantsClosed(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  bool
	}{
		{"", false},
		{"active", false},
		{"suspended", false},
		{"open", false},
		{"closed", true},
		{"all", true},
		{"active,closed", true},
		{" closed ", true},
		{"active,suspended", false},
	} {
		if got := (sessionListRequest{State: tc.state}).wantsClosed(); got != tc.want {
			t.Errorf("wantsClosed(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// TestLoadClosedSessionInfosBoundsHistory pins the two things the bound has to
// do: cap the rows returned, and still find closed rows when the newest session
// beads are all open. Without the openCount headroom a town with a full pool
// would return almost no history.
func TestLoadClosedSessionInfosBoundsHistory(t *testing.T) {
	store := seedSessionListStore(t)
	// Open beads are created FIRST, so on a newest-first read they are the
	// oldest. Then the closed ones, so the newest rows are a mix.
	openCount := 5
	for i := 0; i < openCount; i++ {
		createOpenSessionBead(t, store, fmt.Sprintf("live-%d", i))
	}
	for i := 0; i < 10; i++ {
		createClosedSessionBead(t, store, fmt.Sprintf("ended-%d", i), nil)
	}

	closed, err := loadClosedSessionInfos(store, openCount, 3)
	if err != nil {
		t.Fatalf("loadClosedSessionInfos: %v", err)
	}
	if len(closed) != 3 {
		t.Fatalf("len(closed) = %d, want 3 (the limit)", len(closed))
	}
	for _, in := range closed {
		if !in.Closed {
			t.Fatalf("loadClosedSessionInfos returned an OPEN session: %+v", in)
		}
	}

	// A limit wide enough for the whole history returns all of it, with the
	// open rows still excluded.
	all, err := loadClosedSessionInfos(store, openCount, 50)
	if err != nil {
		t.Fatalf("loadClosedSessionInfos(wide): %v", err)
	}
	if len(all) != 10 {
		t.Fatalf("len(all closed) = %d, want 10", len(all))
	}
}

func TestLoadClosedSessionInfosRefusesAnUnboundedRead(t *testing.T) {
	store := seedSessionListStore(t)
	createClosedSessionBead(t, store, "ended-session", nil)

	// A zero or negative limit is not "everything", it is a caller that has
	// not chosen a bound. Reading every session the town has ever run is
	// never the right default.
	for _, limit := range []int{0, -1} {
		got, err := loadClosedSessionInfos(store, 0, limit)
		if err != nil {
			t.Fatalf("loadClosedSessionInfos(limit=%d): %v", limit, err)
		}
		if len(got) != 0 {
			t.Fatalf("loadClosedSessionInfos(limit=%d) returned %d rows, want 0", limit, len(got))
		}
	}
}

func TestSessionListRequestClosedLimitDefault(t *testing.T) {
	if got := (sessionListRequest{}).closedLimitOrDefault(); got != defaultSessionListClosedLimit {
		t.Fatalf("closedLimitOrDefault() = %d, want %d", got, defaultSessionListClosedLimit)
	}
	if got := (sessionListRequest{ClosedLimit: 7}).closedLimitOrDefault(); got != 7 {
		t.Fatalf("closedLimitOrDefault(7) = %d, want 7", got)
	}
}
