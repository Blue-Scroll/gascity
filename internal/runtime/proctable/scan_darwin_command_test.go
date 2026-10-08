package proctable

import (
	"strings"
	"testing"
)

func TestDarwinPSCommandIgnoresInlineTmuxEnv(t *testing.T) {
	fields := []string{
		"123",
		"45",
		"/bin/bash",
		"GC_SESSION_ID=ga-123",
		"TMUX=/private/tmp/tmux-501/default,1,0",
	}
	if got := darwinPSCommand(fields); got != "/bin/bash" {
		t.Fatalf("darwinPSCommand() = %q, want executable token only", got)
	}
	if isInfrastructureCommand(darwinPSCommand(fields)) {
		t.Fatal("regular shell with TMUX env was classified as infrastructure")
	}
}

func TestDarwinPSCommandStillIdentifiesTmuxExecutable(t *testing.T) {
	fields := []string{"123", "1", "tmux: server", "GC_SESSION_ID=ga-123"}
	if !isInfrastructureCommand(darwinPSCommand(fields)) {
		t.Fatal("tmux executable was not classified as infrastructure")
	}
}

// The infrastructure match is exact on the path-stripped name: the tmux
// executable as ps reports it on Darwin, and the titles tmux sets through
// setproctitle as Linux comm reports them (newline-terminated). Anything
// else — above all a tmux-* wrapper — is a candidate agent root.
func TestIsInfrastructureCommandMatchesExactNamesOnly(t *testing.T) {
	for _, command := range []string{"tmux", "/opt/homebrew/bin/tmux", "tmux: server", "tmux: client", "tmux: server\n"} {
		if !isInfrastructureCommand(command) {
			t.Errorf("isInfrastructureCommand(%q) = false, want true", command)
		}
	}
	for _, command := range []string{"", "claude", "/bin/bash", "tmux-wrapper", "/usr/local/bin/tmux-wrapper", "tmuxinator", "my-tmux"} {
		if isInfrastructureCommand(command) {
			t.Errorf("isInfrastructureCommand(%q) = true, want false: only an exact tmux name is infrastructure", command)
		}
	}
}

// The exact-name match must hold for values the real parser produces, not just
// for pre-split fixtures: psRecords tokenizes the ps line with strings.Fields,
// so a proctitle'd "tmux: server" reaches isInfrastructureCommand as "tmux:".
func TestIsInfrastructureCommandThroughRealPSTokenization(t *testing.T) {
	infra := []string{
		"  100     1 tmux: server",
		"  101     1 /opt/homebrew/bin/tmux -L hq new-session -d",
	}
	for _, line := range infra {
		if !isInfrastructureCommand(darwinPSCommand(strings.Fields(line))) {
			t.Errorf("ps line %q was not classified as infrastructure", line)
		}
	}
	agent := "  102     1 /usr/local/bin/tmux-wrapper --serve GC_SESSION_ID=hq-session"
	if isInfrastructureCommand(darwinPSCommand(strings.Fields(agent))) {
		t.Errorf("ps line %q was classified as infrastructure; a tmux-* wrapper is a candidate agent root", agent)
	}
}

// darwinFixture is the process table from vn-p4rwdst: a tmux server started by
// the deacon's own new-session (so it carries the deacon's GC_SESSION_ID and is
// PPID 1), the deacon's pane shell under it, and a real escaped orphan.
func darwinFixture() map[int]psRecord {
	const deacon = "ga-deacon"
	return map[int]psRecord{
		100: {pid: 100, ppid: 1, command: "tmux", env: map[string]string{"GC_SESSION_ID": deacon}},
		200: {pid: 200, ppid: 100, command: "/bin/zsh", env: map[string]string{"GC_SESSION_ID": deacon}},
		300: {pid: 300, ppid: 1, command: "/bin/zsh", env: map[string]string{"GC_SESSION_ID": deacon}},
	}
}

func TestIsRecordScanRootNeverTheTmuxServer(t *testing.T) {
	records := darwinFixture()
	if isRecordScanRoot(records, records[100]) {
		t.Fatal("tmux server carrying the agent's GC_SESSION_ID was treated as an agent root; killExistingOrphans would kill every session on the socket")
	}
}

func TestIsRecordScanRootKeepsPaneUnderTmuxAndEscapedOrphan(t *testing.T) {
	records := darwinFixture()
	if !isRecordScanRoot(records, records[200]) {
		t.Fatal("pane shell under a tmux server with the same GC_SESSION_ID must stay a root")
	}
	if !isRecordScanRoot(records, records[300]) {
		t.Fatal("escaped PPID-1 orphan must stay a root, or it survives next to its replacement")
	}
}

func TestIsRecordScanRootNeedsSessionID(t *testing.T) {
	records := map[int]psRecord{400: {pid: 400, ppid: 1, command: "/bin/zsh", env: map[string]string{}}}
	if isRecordScanRoot(records, records[400]) {
		t.Fatal("process without GC_SESSION_ID was treated as an agent root")
	}
}
