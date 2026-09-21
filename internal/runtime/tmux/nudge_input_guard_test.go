package tmux

import (
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
