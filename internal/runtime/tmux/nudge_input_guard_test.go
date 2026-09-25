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
	keys  [][]string // every send-keys call
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
			return "", errors.New("unknown variable")
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
		if err := tm.guardUnsentInput("sess"); err != nil {
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
		if err := tm.guardUnsentInput("sess"); err != nil {
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
		err := tm.guardUnsentInput("sess")
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
		if err := tm.guardUnsentInput("sess"); !errors.Is(err, ErrNudgeInputOccupied) {
			t.Fatalf("guard = %v, want a refusal", err)
		}
	})
}
