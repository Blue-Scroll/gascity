//go:build !windows

package gastown_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These pin vn-glc19kd. A full export of a busy scope outlasted the order's
// 900s timeout, gc killed the run's process group, and every killed run left
// its raw export (276 MB for one rig) in the archive's work tree. The next
// commit's `git add -A` staged some of them: 636 leftovers, 48 GB, by
// 2026-10-18.

// exportStallPrelude makes the fake bd export stop inside its issues read,
// after the script has made its temp file, until the test kills it.
const exportStallPrelude = `
case "$*" in
  *"SELECT * FROM"*)
    if [ -n "${DOLT_EXPORT_STARTED:-}" ]; then
      : > "$DOLT_EXPORT_STARTED"
      sleep 30
    fi
    ;;
esac
`

// archiveLeftovers lists every file under the archive's work tree (not .git)
// that is a temp file of any export run, old or new.
func archiveLeftovers(t *testing.T, archiveRepo string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(archiveRepo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && (strings.Contains(d.Name(), ".tmp.") || strings.HasPrefix(d.Name(), ".~")) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", archiveRepo, err)
	}
	return found
}

func writeFileAged(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("leftover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

// startProcess starts a long sleep that the test owns, named so its command
// line reads as the given script.
func startProcess(t *testing.T, scriptName string) int {
	t.Helper()
	path := filepath.Join(t.TempDir(), scriptName)
	writeExecutable(t, path, "#!/bin/sh\nsleep 60\n")
	cmd := exec.Command(path)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", scriptName, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func TestJsonlExportTermMidExportLeavesNoTempFiles(t *testing.T) {
	cityDir := t.TempDir()
	binDir := t.TempDir()
	stateDir := t.TempDir()
	gcLog := filepath.Join(t.TempDir(), "gc.log")
	mailLog := filepath.Join(t.TempDir(), "gc-mail.log")
	archiveRepo := filepath.Join(cityDir, "archive")
	started := filepath.Join(t.TempDir(), "export-started")

	initSeedArchive(t, archiveRepo, 3)
	writeIssuesPayloadDoltStubWithPrelude(t, binDir, `{"rows":[{"id":"c1","title":"cur-1"}]}`, exportStallPrelude)
	writeJsonlExportGCStub(t, binDir)
	env := jsonlExportEnv(t, cityDir, binDir, stateDir, archiveRepo, gcLog, mailLog)
	env["DOLT_EXPORT_STARTED"] = started

	// Run it the way gc's order runner does: its own process group, then
	// SIGTERM to the whole group at the timeout.
	cmd := exec.Command(coreScriptPath("jsonl-export.sh"))
	cmd.Env = mergeTestEnv(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	deadline := time.Now().Add(30 * time.Second)
	for !fileExists(started) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			t.Fatalf("the export never started\n%s", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	tmpDir := filepath.Join(archiveRepo, ".git", "jsonl-export-tmp")
	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("mid-export, %s should hold this run's temp file (err=%v, %d entries)", tmpDir, err, len(entries))
	}

	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatalf("the run did not exit within 10s of SIGTERM; gc would SIGKILL it after 2s\n%s", out.String())
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 143 {
		t.Fatalf("exit = %v, want status 143 from the TERM trap\n%s", err, out.String())
	}

	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("%s survived the TERM (stat err=%v)", tmpDir, err)
	}
	if left := archiveLeftovers(t, archiveRepo); len(left) > 0 {
		t.Fatalf("temp files left in the archive work tree: %v", left)
	}
	if _, err := os.Lstat(filepath.Join(archiveRepo, ".git", "jsonl-export.lock")); !os.IsNotExist(err) {
		t.Fatalf("the run lock survived the TERM (lstat err=%v)", err)
	}
}

func TestJsonlExportSweepsWhatKilledRunsLeftAndCommits(t *testing.T) {
	cityDir := t.TempDir()
	binDir := t.TempDir()
	stateDir := t.TempDir()
	gcLog := filepath.Join(t.TempDir(), "gc.log")
	mailLog := filepath.Join(t.TempDir(), "gc-mail.log")
	archiveRepo := filepath.Join(cityDir, "archive")
	gitDir := filepath.Join(archiveRepo, ".git")

	initSeedArchive(t, archiveRepo, 3)
	// A temp an old run committed, as 268 were between 2026-10-10 and 10-14.
	committedTemp := filepath.Join(archiveRepo, "beads", "export.jsonl.tmp.Commit")
	writeFileAged(t, committedTemp, 0)
	runGit(t, archiveRepo, "add", "-A")
	runGit(t, archiveRepo, "commit", "-q", "-m", "a run that staged its temp")
	prevHead := runGitOut(t, archiveRepo, "rev-parse", "HEAD")
	writeFileAged(t, committedTemp, 2*time.Hour)

	// A SIGKILLed run of this script: its temps in .git, and its lock. The
	// lock's pid now belongs to an unrelated program.
	writeFileAged(t, filepath.Join(gitDir, "jsonl-export-tmp", "beads.export.Dead01"), 0)
	writeFileAged(t, filepath.Join(gitDir, "jsonl-export-tmp", ".~beads.export.Dead01.42"), 0)
	other := startProcess(t, "unrelated-sleeper")
	if err := os.Symlink(strconv.Itoa(other), filepath.Join(gitDir, "jsonl-export.lock")); err != nil {
		t.Fatal(err)
	}
	// Runs of the old script, which kept temps in the scope directory.
	oldRaw := filepath.Join(archiveRepo, "beads", "export.jsonl.tmp.Old001")
	oldBd := filepath.Join(archiveRepo, "beads", ".~export.jsonl.tmp.Old001.7")
	writeFileAged(t, oldRaw, 2*time.Hour)
	writeFileAged(t, oldBd, 2*time.Hour)
	// One that may still be live: the old script ran up to 900s.
	freshLegacy := filepath.Join(archiveRepo, "beads", "issues.jsonl.tmp.Fresh1")
	writeFileAged(t, freshLegacy, 0)
	// A run killed inside `git commit`.
	writeFileAged(t, filepath.Join(gitDir, "index.lock"), 2*time.Hour)

	writeMultiRecordDoltStub(t, binDir, 4)
	writeJsonlExportGCStub(t, binDir)
	env := jsonlExportEnv(t, cityDir, binDir, stateDir, archiveRepo, gcLog, mailLog)

	out, err := runScriptResult(t, coreScriptPath("jsonl-export.sh"), env)
	if err != nil {
		t.Fatalf("jsonl-export failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "clearing the lock of a run that is gone (pid "+strconv.Itoa(other)+")") {
		t.Fatalf("a lock held by a pid that is not jsonl-export must read as stale\n%s", out)
	}

	if head := runGitOut(t, archiveRepo, "rev-parse", "HEAD"); head == prevHead {
		t.Fatalf("no commit landed; a stale index.lock or lock still blocks the archive\n%s", out)
	}
	for _, path := range strings.Split(runGitOut(t, archiveRepo, "ls-tree", "-r", "--name-only", "HEAD"), "\n") {
		if strings.Contains(path, ".tmp.") || strings.Contains(path, ".~") {
			t.Fatalf("HEAD still carries a temp file: %s", path)
		}
	}
	for _, gone := range []string{committedTemp, oldRaw, oldBd, filepath.Join(gitDir, "index.lock"), filepath.Join(gitDir, "jsonl-export-tmp")} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone (lstat err=%v)", gone, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(gitDir, "jsonl-export.lock")); !os.IsNotExist(err) {
		t.Fatalf("the run did not release its lock (lstat err=%v)", err)
	}
	if !fileExists(freshLegacy) {
		t.Fatalf("a legacy temp under an hour old may belong to a live old run and must be kept")
	}
	exclude, err := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil || !strings.Contains(string(exclude), "*.tmp.*") {
		t.Fatalf("info/exclude must hide temp files from git add (err=%v):\n%s", err, exclude)
	}
	state, err := os.ReadFile(filepath.Join(stateDir, "jsonl-export-state.json"))
	if err != nil || !strings.Contains(string(state), `"last_full_export_at":"`) {
		t.Fatalf("a full export must stamp last_full_export_at for gc doctor (err=%v): %s", err, state)
	}
}

func TestJsonlExportSkipsWhileALiveRunHoldsTheLock(t *testing.T) {
	cityDir := t.TempDir()
	binDir := t.TempDir()
	stateDir := t.TempDir()
	gcLog := filepath.Join(t.TempDir(), "gc.log")
	mailLog := filepath.Join(t.TempDir(), "gc-mail.log")
	archiveRepo := filepath.Join(cityDir, "archive")
	gitDir := filepath.Join(archiveRepo, ".git")
	outcomeFile := filepath.Join(t.TempDir(), "outcome.json")

	prevHead := initSeedArchive(t, archiveRepo, 3)
	holder := startProcess(t, "jsonl-export.sh")
	if err := os.Symlink(strconv.Itoa(holder), filepath.Join(gitDir, "jsonl-export.lock")); err != nil {
		t.Fatal(err)
	}
	liveTemp := filepath.Join(gitDir, "jsonl-export-tmp", "beads.export.Live01")
	writeFileAged(t, liveTemp, 0)

	writeMultiRecordDoltStub(t, binDir, 4)
	writeJsonlExportGCStub(t, binDir)
	env := jsonlExportEnv(t, cityDir, binDir, stateDir, archiveRepo, gcLog, mailLog)
	env["GC_ORDER_OUTCOME_FILE"] = outcomeFile

	out, err := runScriptResult(t, coreScriptPath("jsonl-export.sh"), env)
	if err != nil {
		t.Fatalf("a skipped run must exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "run "+strconv.Itoa(holder)+" is still exporting") {
		t.Fatalf("expected the skip to name the live run\n%s", out)
	}
	if head := runGitOut(t, archiveRepo, "rev-parse", "HEAD"); head != prevHead {
		t.Fatalf("a skipped run committed: HEAD %s, want %s", head, prevHead)
	}
	if gcData, _ := os.ReadFile(gcLog); strings.Contains(string(gcData), "export") {
		t.Fatalf("a skipped run must not export:\n%s", gcData)
	}
	if !fileExists(liveTemp) {
		t.Fatalf("a skipped run deleted the live run's temp file")
	}
	if target, err := os.Readlink(filepath.Join(gitDir, "jsonl-export.lock")); err != nil || target != strconv.Itoa(holder) {
		t.Fatalf("the live run's lock was touched: %q, %v", target, err)
	}
	outcome, err := os.ReadFile(outcomeFile)
	if err != nil || !strings.Contains(string(outcome), `"outcome":"skipped"`) {
		t.Fatalf("a skipped run must declare outcome skipped (err=%v): %s", err, outcome)
	}
}
