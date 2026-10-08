package tmux

import (
	"slices"
	"testing"
	"time"
)

// A short text with a line break must still go in as ONE bracketed paste,
// never as typed keys (hq-q1cpc). Typed with send-keys -l, claude's input box
// guesses where the "paste" starts and throws away the front: 233 of 1454
// typed deferred-reminder nudges to the mayor and deacon arrived cut, some down
// to the bare line "</system-reminder>". Every nudge sent as a bracketed paste
// (0 of 150) arrived whole.
func TestSendKeysLiteralWithRetryPastesMultiLineText(t *testing.T) {
	for _, text := range []string{
		"<system-reminder>\nYou have a deferred reminder.\n</system-reminder>\n",
		"first line\r\nsecond line",
		"ends with a carriage return\r",
	} {
		fe := &fakeExecutor{}
		tm := NewTmuxWithConfig(DefaultConfig())
		tm.exec = fe

		if err := tm.sendKeysLiteralWithRetry("%1", text, time.Second); err != nil {
			t.Fatalf("sendKeysLiteralWithRetry(%q) = %v, want nil", text, err)
		}
		if len(fe.calls) != 2 {
			t.Fatalf("text %q: tmux calls = %#v, want load-buffer then paste-buffer", text, fe.calls)
		}
		if !slices.Contains(fe.calls[0], "load-buffer") {
			t.Fatalf("text %q: first call = %v, want load-buffer", text, fe.calls[0])
		}
		if !slices.Contains(fe.calls[1], "paste-buffer") || !slices.Contains(fe.calls[1], "-p") {
			t.Fatalf("text %q: second call = %v, want paste-buffer -p (bracketed paste)", text, fe.calls[1])
		}
	}
}

// One line of ordinary length still goes as typed keys. This pins the other
// side of the rule so a later change cannot quietly paste everything.
func TestSendKeysLiteralWithRetryTypesOneShortLine(t *testing.T) {
	fe := &fakeExecutor{}
	tm := NewTmuxWithConfig(DefaultConfig())
	tm.exec = fe

	if err := tm.sendKeysLiteralWithRetry("%1", "continue", time.Second); err != nil {
		t.Fatalf("sendKeysLiteralWithRetry() = %v, want nil", err)
	}
	if len(fe.calls) != 1 || !slices.Contains(fe.calls[0], "send-keys") || !slices.Contains(fe.calls[0], "-l") {
		t.Fatalf("tmux calls = %#v, want one send-keys -l", fe.calls)
	}
}
