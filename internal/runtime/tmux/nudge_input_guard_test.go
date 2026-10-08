package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The box lines below are copied from real Claude panes in this town
// (capture-pane -e, 2026-09-21). U+00A0 is the no-break space Claude prints
// after its prompt glyph.
func TestInputBoxText(t *testing.T) {
	const esc = "\x1b"
	cases := []struct {
		name      string
		lines     []string
		wantText  string
		wantFound bool
	}{
		{
			name:      "dim placeholder with a color code inside the dim run",
			lines:     []string{esc + "[38;5;246m❯\u00a0" + esc + "[2m" + esc + "[39mPress up to edit queued messages" + esc + "[0m"},
			wantFound: true,
		},
		{
			name:      "dim placeholder drawn one word at a time",
			lines:     []string{esc + "[38;5;246m❯\u00a0" + esc + "[2mPress" + esc + "[0m " + esc + "[2mup" + esc + "[0m " + esc + "[2mto" + esc + "[0m " + esc + "[2medit" + esc + "[0m"},
			wantFound: true,
		},
		{
			name:      "real typing is not dim",
			lines:     []string{esc + "[38;5;246m❯\u00a0" + esc + "[39mapprove both PRs"},
			wantText:  "approve both PRs",
			wantFound: true,
		},
		{
			name: "queued messages above the box do not count; the last prompt line is the box",
			lines: []string{
				esc + "[38;5;239m" + esc + "[48;5;237m❯ " + esc + "[38;5;246mRun gc hook" + esc + "[39m",
				"",
				esc + "[38;5;246m❯\u00a0" + esc + "[2mPress up to edit queued messages" + esc + "[0m",
			},
			wantFound: true,
		},
		{
			name:      "a 256-color index of 2 is a color, not dim",
			lines:     []string{"❯ " + esc + "[38;5;2mhello" + esc + "[0m"},
			wantText:  "hello",
			wantFound: true,
		},
		{
			name:      "bold-off (22) also ends dim",
			lines:     []string{"❯ " + esc + "[2mhint" + esc + "[22mtyped"},
			wantText:  "typed",
			wantFound: true,
		},
		{
			name:      "a box drawn inside a border (grok)",
			lines:     []string{"│ ❯ hi there │"},
			wantText:  "hi there",
			wantFound: true,
		},
		{
			// Copied from a resumed polecat pane (capture-pane -e, 2026-09-25).
			// Before the box is drawn, the only prompt line is the bubble for
			// gc's own startup prompt. It is 35 + len(agent name) characters,
			// the exact length every refused resume nudge reported (hq-51ocez).
			name: "a resumed screen with only message bubbles has no box yet",
			lines: []string{
				esc + "[38;5;239m" + esc + "[48;5;237m❯ " + esc + "[38;5;246m[bluescroll] vessel-network/dag • 2026-09-24T23:10:41" + esc + "[39m",
				"",
				esc + "[38;5;239m" + esc + "[48;5;237m❯ " + esc + "[38;5;231mRun gc hook; it checks assigned work first, then routed pool work." + esc + "[39m",
			},
		},
		{
			name:  "a truecolor background is a bubble too",
			lines: []string{esc + "[48;2;58;58;58m❯ approve both PRs" + esc + "[0m"},
		},
		{
			name:  "a 16-color background is a bubble too",
			lines: []string{esc + "[100m❯ approve both PRs" + esc + "[0m"},
		},
		{
			name:      "a background turned off before the glyph is the box",
			lines:     []string{esc + "[48;5;237m" + esc + "[49m❯\u00a0approve both PRs"},
			wantText:  "approve both PRs",
			wantFound: true,
		},
		{
			name:      "a background color on the words after the glyph does not hide the box",
			lines:     []string{"❯\u00a0" + esc + "[48;5;237mapprove both PRs" + esc + "[0m"},
			wantText:  "approve both PRs",
			wantFound: true,
		},
		{
			name:  "no prompt line means no box to guard",
			lines: []string{"$ ls", "file.txt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := inputBoxText(tc.lines, DefaultReadyPromptPrefix)
			if got != tc.wantText || found != tc.wantFound {
				t.Fatalf("inputBoxText = (%q, %v), want (%q, %v)", got, found, tc.wantText, tc.wantFound)
			}
		})
	}
}

func TestProbeVerdict(t *testing.T) {
	cases := []struct {
		pre, probed string
		want        inputBoxVerdict
	}{
		{"approve both PRs", "x", boxGhost},
		{"approve both PRs", "approve both PRsx", boxReal},
		{"approve both PRs", "approve both PRs", boxUnknown}, // the probe never landed
		{"approve both PRs", "", boxUnknown},
		{"a long line cut off with…", "something else", boxUnknown},
	}
	for _, tc := range cases {
		if got := probeVerdict(tc.pre, tc.probed); got != tc.want {
			t.Errorf("probeVerdict(%q, %q) = %s, want %s", tc.pre, tc.probed, got, tc.want)
		}
	}
}

func TestIsOwnDraft(t *testing.T) {
	long := "Run gc hook; it checks assigned work first, then routed pool work, then mail."
	head := nudgeDraftHead(long)
	if n := len([]rune(head)); n != nudgeDraftHeadRunes {
		t.Fatalf("nudgeDraftHead kept %d runes, want %d", n, nudgeDraftHeadRunes)
	}
	cases := []struct {
		box, head string
		want      bool
	}{
		{long, head, true},      // the whole message fits the box
		{long[:60], head, true}, // only the first line of a wrapped message shows
		{"Run gc hook", nudgeDraftHead("Run gc hook"), true},
		{"Run  gc hook", nudgeDraftHead("Run gc\nhook"), true}, // spacing and newlines do not matter
		{"approve both PRs", head, false},                      // a person's words
		{"R", head, false},                                     // a person typing the same first letter
		{"approve both PRs", "", false},                        // gc never pasted here

		// Claude shows a multi-line paste as a label (hq-eez8jf). A label
		// head must match the box exactly.
		{"[Pasted text #3 +8 lines]", "[Pasted text #3 +8 lines]", true},
		{"[Pasted text #3 +8 lines] [Pasted text #4 +2 lines]", "[Pasted text #3 +8 lines] [Pasted text #4 +2 lines]", true},
		{"[Pasted text #4 +8 lines]", "[Pasted text #3 +8 lines]", false},         // a later paste, not gc's
		{"[Pasted text #3 +8 lines] approve", "[Pasted text #3 +8 lines]", false}, // a person typed after it
		{"[Pasted text #3 +8 lines]", head, false},                                // gc's last paste was text
	}
	for _, tc := range cases {
		if got := isOwnDraft(tc.box, tc.head); got != tc.want {
			t.Errorf("isOwnDraft(%q, %q) = %v, want %v", tc.box, tc.head, got, tc.want)
		}
	}
}

func TestErrNudgeInputOccupiedNeverCarriesTheText(t *testing.T) {
	// guardUnsentInput builds its error from the target, a length and a
	// verdict only. Pin the wrap so callers can errors.Is it.
	err := wrapOccupied("sess", "approve both PRs", boxReal)
	if !errors.Is(err, ErrNudgeInputOccupied) {
		t.Fatalf("error %v does not wrap ErrNudgeInputOccupied", err)
	}
	if strings.Contains(err.Error(), "approve") {
		t.Fatalf("error text leaks the stranded line: %v", err)
	}
}

// boxFake is a tmux that draws one Claude screen and keeps an input box. The
// probe's "x" and BSpace change the box the way a real one would, so the
// guard's whole path runs: capture, env read, probe, restore.
type boxFake struct {
	above []string // lines drawn above the box (history bubbles)
	box   string   // what the box holds; "" draws no box at all when noBox
	dim   string   // a dim suggestion drawn after the box text
	noBox bool
	keys  [][]string        // every send-keys call
	env   map[string]string // set-environment writes, read back by show-environment
}

func (f *boxFake) screen() string {
	lines := append([]string(nil), f.above...)
	if !f.noBox {
		line := "\x1b[38;5;246m❯\u00a0\x1b[39m" + f.box
		if f.dim != "" {
			line += "\x1b[2m" + f.dim + "\x1b[0m"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (f *boxFake) execute(args []string) (string, error) {
	for i, a := range args {
		switch a {
		case "show-environment":
			key := args[len(args)-1]
			if v, ok := f.env[key]; ok {
				return key + "=" + v, nil
			}
			return "", errors.New("unknown variable")
		case "set-environment":
			if f.env == nil {
				f.env = map[string]string{}
			}
			if args[len(args)-2] == "-u" {
				delete(f.env, args[len(args)-1])
			} else {
				f.env[args[len(args)-2]] = args[len(args)-1]
			}
			return "", nil
		case "capture-pane":
			return f.screen(), nil
		case "send-keys":
			f.keys = append(f.keys, append([]string(nil), args[i:]...))
			last := args[len(args)-1]
			if last == "BSpace" {
				if r := []rune(f.box); len(r) > 0 {
					f.box = string(r[:len(r)-1])
				}
			} else {
				f.box += last
				f.dim = "" // typing replaces a suggestion
			}
			return "", nil
		}
	}
	return "", nil
}

func (f *boxFake) executeCtx(_ context.Context, args []string) (string, error) {
	return f.execute(args)
}

func TestGuardUnsentInput(t *testing.T) {
	const bubble = "\x1b[38;5;239m\x1b[48;5;237m❯ \x1b[38;5;246m[bluescroll] vessel-network/dag • 2026-09-24T23:10:41\x1b[39m"

	t.Run("a resume screen with no box yet lets the nudge through and types nothing", func(t *testing.T) {
		f := &boxFake{above: []string{bubble}, noBox: true}
		tm := NewTmux()
		tm.exec = f
		if _, err := tm.guardUnsentInput("sess", "Run gc hook"); err != nil {
			t.Fatalf("guard refused a resume screen: %v", err)
		}
		if len(f.keys) != 0 {
			t.Fatalf("guard typed into a pane with no box: %v", f.keys)
		}
	})

	t.Run("a dim suggestion in the box lets the nudge through and types nothing", func(t *testing.T) {
		f := &boxFake{above: []string{bubble}, dim: "Run 'gc prime' to check merge queue and begin processing."}
		tm := NewTmux()
		tm.exec = f
		if _, err := tm.guardUnsentInput("sess", "Run gc hook"); err != nil {
			t.Fatalf("guard refused a dim suggestion: %v", err)
		}
		if len(f.keys) != 0 {
			t.Fatalf("guard probed a box holding only dim text: %v", f.keys)
		}
	})

	t.Run("bright typed words still refuse, and the probe is taken back out", func(t *testing.T) {
		f := &boxFake{above: []string{bubble}, box: "approve both PRs"}
		tm := NewTmux()
		tm.exec = f
		_, err := tm.guardUnsentInput("sess", "Run gc hook")
		if !errors.Is(err, ErrNudgeInputOccupied) || !strings.Contains(err.Error(), "verdict real") {
			t.Fatalf("guard = %v, want a refusal with verdict real", err)
		}
		if f.box != "approve both PRs" {
			t.Fatalf("box after the probe = %q, want the person's words back unchanged", f.box)
		}
	})

	t.Run("bright words followed by a dim suggestion still refuse", func(t *testing.T) {
		f := &boxFake{box: "approve both PRs", dim: " and merge them"}
		tm := NewTmux()
		tm.exec = f
		if _, err := tm.guardUnsentInput("sess", "Run gc hook"); !errors.Is(err, ErrNudgeInputOccupied) {
			t.Fatalf("guard = %v, want a refusal", err)
		}
	})
}

func TestIsPasteLabelOnly(t *testing.T) {
	cases := map[string]bool{
		"[Pasted text #3 +8 lines]":                           true,
		"[Pasted text #12 +1 line]":                           true,
		"[Pasted text #2]":                                    true,
		"[Pasted text #3 +8 lines][Pasted text #4 +2 lines]":  true,
		"[Pasted text #3 +8 lines] [Pasted text #4 +2 lines]": true,
		"[Pasted text #3 +8 lines] approve":                   false,
		"approve [Pasted text #3 +8 lines]":                   false,
		"[Pasted text #x +8 lines]":                           false,
		"":                                                    false,
	}
	for box, want := range cases {
		if got := isPasteLabelOnly(box); got != want {
			t.Errorf("isPasteLabelOnly(%q) = %v, want %v", box, got, want)
		}
	}
}

// TestGuardCollapsedPaste covers hq-eez8jf: one lost Enter after a multi-line
// nudge left Claude's "[Pasted text #3 +8 lines]" in the mayor's box, and the
// guard refused every message after it for 7 hours.
func TestGuardCollapsedPaste(t *testing.T) {
	const label = "[Pasted text #3 +8 lines]"
	const msg = "line one\nline two"

	// ownPaste is a box holding gc's own stranded paste of msg, recorded the
	// way NudgeSession records it: the draft first, then the label it drew.
	ownPaste := func(t *testing.T) (*Tmux, *boxFake) {
		t.Helper()
		f := &boxFake{}
		tm := NewTmux()
		tm.exec = f
		if err := tm.recordNudgeDraft("sess", msg); err != nil {
			t.Fatalf("recordNudgeDraft: %v", err)
		}
		f.box = label
		if got := tm.notePasteLabel("sess"); got != label {
			t.Fatalf("notePasteLabel = %q, want %q", got, label)
		}
		f.keys = nil
		return tm, f
	}

	t.Run("gc's own label for the same message is submitted, not pasted again", func(t *testing.T) {
		tm, f := ownPaste(t)
		drafted, err := tm.guardUnsentInput("sess", msg)
		if err != nil || !drafted {
			t.Fatalf("guard = (%v, %v), want (true, nil)", drafted, err)
		}
		if len(f.keys) != 0 {
			t.Fatalf("guard typed into gc's own draft: %v", f.keys)
		}
		if got := tm.draftPasteLabel("sess"); got != label {
			t.Fatalf("draftPasteLabel = %q, want %q", got, label)
		}
	})

	t.Run("gc's own label lets a different message through", func(t *testing.T) {
		tm, f := ownPaste(t)
		drafted, err := tm.guardUnsentInput("sess", "a newer message")
		if err != nil || drafted {
			t.Fatalf("guard = (%v, %v), want (false, nil)", drafted, err)
		}
		if len(f.keys) != 0 {
			t.Fatalf("guard probed gc's own draft: %v", f.keys)
		}
	})

	t.Run("a label with no gc paste on record refuses", func(t *testing.T) {
		f := &boxFake{box: label}
		tm := NewTmux()
		tm.exec = f
		_, err := tm.guardUnsentInput("sess", msg)
		if !errors.Is(err, ErrNudgeInputOccupied) {
			t.Fatalf("guard = %v, want a refusal", err)
		}
	})

	t.Run("a label gc did not draw refuses, even after a gc paste", func(t *testing.T) {
		tm, f := ownPaste(t)
		f.box = "[Pasted text #4 +8 lines]" // a person's later paste
		if _, err := tm.guardUnsentInput("sess", msg); !errors.Is(err, ErrNudgeInputOccupied) {
			t.Fatalf("guard = %v, want a refusal", err)
		}
	})

	t.Run("a label is not gc's when gc's last paste was recorded as text", func(t *testing.T) {
		f := &boxFake{}
		tm := NewTmux()
		tm.exec = f
		_ = tm.recordNudgeDraft("sess", msg)
		f.box = label
		if _, err := tm.guardUnsentInput("sess", msg); !errors.Is(err, ErrNudgeInputOccupied) {
			t.Fatalf("guard = %v, want a refusal", err)
		}
	})

	t.Run("a real typed line after gc's label still refuses", func(t *testing.T) {
		tm, f := ownPaste(t)
		f.box = label + " approve both PRs"
		if _, err := tm.guardUnsentInput("sess", msg); !errors.Is(err, ErrNudgeInputOccupied) {
			t.Fatalf("guard = %v, want a refusal", err)
		}
		if f.box != label+" approve both PRs" {
			t.Fatalf("box after the probe = %q, want it unchanged", f.box)
		}
	})

	t.Run("notePasteLabel records nothing when the box shows text", func(t *testing.T) {
		f := &boxFake{}
		tm := NewTmux()
		tm.exec = f
		_ = tm.recordNudgeDraft("sess", "Run gc hook")
		f.box = "Run gc hook"
		if got := tm.notePasteLabel("sess"); got != "" {
			t.Fatalf("notePasteLabel = %q, want nothing", got)
		}
		if got := f.env[nudgeDraftEnvKey]; got != "Run gc hook" {
			t.Fatalf("draft head = %q, want the text head kept", got)
		}
	})
}

func TestSubmitStrandedPaste(t *testing.T) {
	const label = "[Pasted text #3 +8 lines]"

	t.Run("no label on record: the box is not read", func(t *testing.T) {
		f := &boxFake{box: label}
		tm := NewTmux()
		tm.exec = f
		sends := 0
		if got := tm.submitStrandedPaste("sess", "", func() error { sends++; return nil }); got != pasteNotTracked || sends != 0 {
			t.Fatalf("got (%v, %d sends), want (pasteNotTracked, 0)", got, sends)
		}
	})

	t.Run("a box already empty proves the submit", func(t *testing.T) {
		f := &boxFake{}
		tm := NewTmux()
		tm.exec = f
		sends := 0
		if got := tm.submitStrandedPaste("sess", label, func() error { sends++; return nil }); got != pasteSubmitted || sends != 0 {
			t.Fatalf("got (%v, %d sends), want (pasteSubmitted, 0)", got, sends)
		}
	})

	t.Run("a lost Enter is sent again until the label leaves", func(t *testing.T) {
		f := &boxFake{box: label}
		tm := NewTmux()
		tm.exec = f
		sends := 0
		enter := func() error { sends++; f.box = ""; return nil }
		if got := tm.submitStrandedPaste("sess", label, enter); got != pasteSubmitted || sends != 1 {
			t.Fatalf("got (%v, %d sends), want (pasteSubmitted, 1)", got, sends)
		}
	})

	t.Run("an Enter that never lands reads as stranded", func(t *testing.T) {
		f := &boxFake{box: label}
		tm := NewTmux()
		tm.exec = f
		sends := 0
		if got := tm.submitStrandedPaste("sess", label, func() error { sends++; return nil }); got != pasteStranded || sends != pasteResubmitMax {
			t.Fatalf("got (%v, %d sends), want (pasteStranded, %d)", got, sends, pasteResubmitMax)
		}
	})

	t.Run("somebody typing stops the re-submit", func(t *testing.T) {
		f := &boxFake{box: label + " approve"}
		tm := NewTmux()
		tm.exec = f
		sends := 0
		if got := tm.submitStrandedPaste("sess", label, func() error { sends++; return nil }); got != pasteNotTracked || sends != 0 {
			t.Fatalf("got (%v, %d sends), want (pasteNotTracked, 0)", got, sends)
		}
	})

	t.Run("no box on screen stops the re-submit", func(t *testing.T) {
		f := &boxFake{noBox: true}
		tm := NewTmux()
		tm.exec = f
		sends := 0
		if got := tm.submitStrandedPaste("sess", label, func() error { sends++; return nil }); got != pasteNotTracked || sends != 0 {
			t.Fatalf("got (%v, %d sends), want (pasteNotTracked, 0)", got, sends)
		}
	})
}
