//go:build integration

package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNudgeUnsentInputRealTmux proves, on a real throwaway tmux server, that a
// nudge never submits text somebody typed and did not send (hq-ibmvf).
//
// The pane runs a tiny line reader that draws Claude's prompt glyph and writes
// every line it receives to a file. So the file holds exactly what a nudge
// SUBMITTED. Before the guard, a nudge pasted after the typed words and pressed
// Enter, and the file got "approve both PRs" glued to the nudge text.
func TestNudgeUnsentInputRealTmux(t *testing.T) {
	if os.Getenv("GC_TMUX_INTEGRATION") != "1" {
		t.Skip("set GC_TMUX_INTEGRATION=1 to run this real-tmux test (spins a throwaway tmux server)")
	}

	// start makes a session whose pane reads lines behind a "❯ " prompt and
	// appends each one to the returned file. hint, when set, is drawn DIM after
	// the prompt, the way Claude draws its placeholder in an empty box.
	start := func(t *testing.T, socket, hint string) (*Tmux, string, string) {
		t.Helper()
		tm := NewTmuxWithConfig(Config{SocketName: socket, NudgeReadyTimeout: 5 * time.Second, NudgeLockTimeout: 5 * time.Second})
		const sess = "unsent-input"
		_, _ = tm.run("kill-server")
		t.Cleanup(func() { _, _ = tm.run("kill-server") })
		got := filepath.Join(t.TempDir(), "submitted.txt")
		script := `while :; do printf '❯ '; ` +
			`[ -n "$HINT" ] && printf '\033[2m%s\033[0m\r❯ ' "$HINT"; ` +
			`IFS= read -r l || exit; printf '%s\n' "$l" >> "$OUT"; done`
		if _, err := tm.run("new-session", "-d", "-s", sess, "-x", "120", "-y", "24",
			"-e", "OUT="+got, "-e", "HINT="+hint, "bash", "--norc", "--noprofile", "-c", script); err != nil {
			t.Skipf("cannot create tmux session (tmux unavailable?): %v", err)
		}
		time.Sleep(400 * time.Millisecond)
		return tm, sess, got
	}
	submitted := func(t *testing.T, path string) string {
		t.Helper()
		time.Sleep(700 * time.Millisecond)
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read submitted lines: %v", err)
		}
		// A pane with no GC_PROVIDER gets an Escape before Enter
		// (shouldSendEscapeBeforeEnter), and this plain reader records it.
		return strings.ReplaceAll(string(b), "\x1b", "")
	}

	t.Run("typed and unsent words are refused, never submitted", func(t *testing.T) {
		tm, sess, got := start(t, "gcunsenta", "")
		if _, err := tm.run("send-keys", "-t", sess, "-l", "approve both PRs"); err != nil {
			t.Fatalf("type the human line: %v", err)
		}
		time.Sleep(300 * time.Millisecond)

		err := tm.NudgeSession(sess, "Run gc hook")
		if !errors.Is(err, ErrNudgeInputOccupied) {
			t.Fatalf("NudgeSession error = %v, want ErrNudgeInputOccupied", err)
		}
		if s := submitted(t, got); s != "" {
			t.Fatalf("the nudge submitted %q; nothing may be submitted while a human line sits unsent", s)
		}
		// The human's words must still be there, untouched: the probe cleans up.
		lines, _ := tm.CapturePaneLines(sess, 5)
		if box := strings.Join(lines, "\n"); !strings.Contains(box, "❯ approve both PRs") || strings.Contains(box, "approve both PRsx") {
			t.Fatalf("the human line was changed; pane now reads:\n%s", box)
		}
		// The error must not carry the human's words to the caller (core rule 5:
		// stranded text goes to a human, never relayed to an agent).
		if strings.Contains(err.Error(), "approve") {
			t.Fatalf("error text leaks the stranded line: %v", err)
		}
	})

	t.Run("an empty box still gets the nudge", func(t *testing.T) {
		tm, sess, got := start(t, "gcunsentb", "")
		if err := tm.NudgeSession(sess, "Run gc hook"); err != nil {
			t.Fatalf("NudgeSession on an empty box: %v", err)
		}
		if s := submitted(t, got); s != "Run gc hook\n" {
			t.Fatalf("submitted %q, want exactly the nudge", s)
		}
	})

	t.Run("a dim placeholder is not typing", func(t *testing.T) {
		tm, sess, got := start(t, "gcunsentc", "Press up to edit queued messages")
		if err := tm.NudgeSession(sess, "Run gc hook"); err != nil {
			t.Fatalf("NudgeSession behind a dim placeholder: %v", err)
		}
		if s := submitted(t, got); s != "Run gc hook\n" {
			t.Fatalf("submitted %q, want exactly the nudge", s)
		}
	})

	t.Run("gc's own unsubmitted draft does not block the retry", func(t *testing.T) {
		tm, sess, got := start(t, "gcunsentd", "")
		// A lost Enter leaves gc's own paste drafted in the box (ga-bwm). The
		// queue then retries the same nudge. That retry must not be refused.
		msg := "Run gc hook"
		if err := tm.recordNudgeDraft(sess, msg); err != nil {
			t.Fatalf("recordNudgeDraft: %v", err)
		}
		if _, err := tm.run("send-keys", "-t", sess, "-l", msg); err != nil {
			t.Fatalf("draft: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
		if err := tm.NudgeSession(sess, msg); err != nil {
			t.Fatalf("retry over gc's own draft: %v", err)
		}
		if s := submitted(t, got); !strings.HasPrefix(s, msg) {
			t.Fatalf("submitted %q, want the retried nudge", s)
		}
	})
}
