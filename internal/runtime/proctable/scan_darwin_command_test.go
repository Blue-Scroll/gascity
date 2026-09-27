package proctable

import "testing"

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

func TestDarwinScanRootNeverTheTmuxServer(t *testing.T) {
	records := darwinFixture()
	if darwinScanRoot(records, records[100]) {
		t.Fatal("tmux server carrying the agent's GC_SESSION_ID was treated as an agent root; killExistingOrphans would kill every session on the socket")
	}
}

func TestDarwinScanRootKeepsPaneUnderTmuxAndEscapedOrphan(t *testing.T) {
	records := darwinFixture()
	if !darwinScanRoot(records, records[200]) {
		t.Fatal("pane shell under a tmux server with the same GC_SESSION_ID must stay a root")
	}
	if !darwinScanRoot(records, records[300]) {
		t.Fatal("escaped PPID-1 orphan must stay a root, or it survives next to its replacement")
	}
}

func TestDarwinScanRootNeedsSessionID(t *testing.T) {
	records := map[int]psRecord{400: {pid: 400, ppid: 1, command: "/bin/zsh", env: map[string]string{}}}
	if darwinScanRoot(records, records[400]) {
		t.Fatal("process without GC_SESSION_ID was treated as an agent root")
	}
}
