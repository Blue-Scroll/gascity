//go:build linux

package proctable

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// buildFakeProc builds a minimal /proc-shaped fixture tree under root for pid
// with parent PID 1 (init). environ is written as NUL-delimited key=value pairs.
func buildFakeProc(t *testing.T, root string, pid int, env map[string]string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	var buf []byte
	for k, v := range env {
		buf = append(buf, []byte(k+"="+v+"\x00")...)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), buf, 0o644); err != nil {
		t.Fatalf("write environ: %v", err)
	}
	// stat: ppid=1 (init) so this process is classified as a root.
	stat := strconv.Itoa(pid) + " (cmd) S 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
}

func TestScanWithRootStatVanished(t *testing.T) {
	root := t.TempDir()
	pid := 500
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write environ but no stat file (process died between environ read and stat check).
	env := []byte("GC_SESSION_ID=ga-test\x00")
	if err := os.WriteFile(filepath.Join(dir, "environ"), env, 0o644); err != nil {
		t.Fatalf("write environ: %v", err)
	}
	// stat file absent — simulates TOCTOU race.

	got, err := scanWithRoot(root, "ga-test")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("scanWithRoot returned %d runtimes for a vanished process, want 0", len(got))
	}
}

func TestScanWithRootEmptyReturnsNonNilSlice(t *testing.T) {
	root := t.TempDir()
	got, err := scanWithRoot(root, "")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if got == nil {
		t.Fatal("scanWithRoot returned nil slice, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("scanWithRoot returned %d runtimes, want 0", len(got))
	}
}

func TestScanWithRootFiltersBySessionID(t *testing.T) {
	root := t.TempDir()
	// pid 100: parent 1 (init), session ga-abc
	buildFakeProc(t, root, 100, map[string]string{"GC_SESSION_ID": "ga-abc"})
	// pid 200: parent 1 (init), session ga-xyz
	buildFakeProc(t, root, 200, map[string]string{"GC_SESSION_ID": "ga-xyz"})

	got, err := scanWithRoot(root, "ga-abc")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "ga-abc" || got[0].PID != 100 {
		t.Fatalf("scanWithRoot = %v, want [{ga-abc pid=100}]", got)
	}
}

func TestScanWithRootEmptyIDReturnsAll(t *testing.T) {
	root := t.TempDir()
	buildFakeProc(t, root, 100, map[string]string{"GC_SESSION_ID": "ga-abc"})
	buildFakeProc(t, root, 200, map[string]string{"GC_SESSION_ID": "ga-xyz"})

	got, err := scanWithRoot(root, "")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("scanWithRoot = %d entries, want 2", len(got))
	}
}

func TestScanWithRootParsesEpoch(t *testing.T) {
	root := t.TempDir()
	buildFakeProc(t, root, 300, map[string]string{
		"GC_SESSION_ID":    "ga-epoch",
		"GC_RUNTIME_EPOCH": "42",
	})

	got, err := scanWithRoot(root, "ga-epoch")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Epoch != 42 {
		t.Fatalf("Epoch = %d, want 42", got[0].Epoch)
	}
}

func TestScanWithRootPopulatesCityFromGCPath(t *testing.T) {
	root := t.TempDir()
	buildFakeProc(t, root, 310, map[string]string{
		"GC_SESSION_ID": "ga-city",
		"GC_CITY_PATH":  "/tmp/primary-city",
		"GC_CITY":       "/tmp/fallback-city",
	})

	got, err := scanWithRoot(root, "ga-city")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].City != "/tmp/primary-city" {
		t.Fatalf("City = %q, want GC_CITY_PATH value", got[0].City)
	}
}

func TestScanWithRootPopulatesCityFromGCCityFallback(t *testing.T) {
	root := t.TempDir()
	buildFakeProc(t, root, 320, map[string]string{
		"GC_SESSION_ID": "ga-city",
		"GC_CITY":       "/tmp/fallback-city",
	})

	got, err := scanWithRoot(root, "ga-city")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].City != "/tmp/fallback-city" {
		t.Fatalf("City = %q, want GC_CITY fallback value", got[0].City)
	}
}

func TestScanWithRootMissingEnvironSkipped(t *testing.T) {
	root := t.TempDir()
	// Directory exists but no environ (ENOENT) — should be skipped without error.
	dir := filepath.Join(root, "400")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := scanWithRoot(root, "")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d entries, want 0", len(got))
	}
}

// setFakeProcParent rewrites pid's stat with the given parent and writes its
// comm, for fixtures that need more than buildFakeProc's PPID-1 default.
func setFakeProcParent(t *testing.T, root string, pid, ppid int, comm string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	stat := strconv.Itoa(pid) + " (cmd) S " + strconv.Itoa(ppid) + " 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
		t.Fatalf("write comm: %v", err)
	}
}

// The process table from vn-p4rwdst: a tmux server that carries the deacon's
// GC_SESSION_ID and is PPID 1, the deacon's pane shell under it, and a real
// escaped orphan. Only the server must drop out; killing it kills every
// session on the socket.
func TestScanWithRootNeverReturnsTheTmuxServer(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"GC_SESSION_ID": "ga-deacon"}
	buildFakeProc(t, root, 100, env)
	setFakeProcParent(t, root, 100, 1, "tmux: server")
	buildFakeProc(t, root, 200, env)
	setFakeProcParent(t, root, 200, 100, "zsh")
	buildFakeProc(t, root, 300, env)
	setFakeProcParent(t, root, 300, 1, "zsh")

	got, err := scanWithRoot(root, "ga-deacon")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	var pids []int
	for _, r := range got {
		pids = append(pids, r.PID)
	}
	if len(pids) != 2 || pids[0] != 200 || pids[1] != 300 {
		t.Fatalf("scanWithRoot roots = %v, want [200 300] (pane under tmux + escaped orphan, never the tmux server 100)", pids)
	}
}
