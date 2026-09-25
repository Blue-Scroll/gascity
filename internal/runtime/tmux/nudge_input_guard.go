package tmux

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrNudgeInputOccupied means a nudge was refused because the agent's input
// box already holds text that gc did not type (hq-ibmvf).
//
// A nudge pastes its message and presses Enter. Enter submits the WHOLE box,
// so any words a person typed and did not send go out glued to the nudge. In
// this town that fired, or nearly fired, "approve both PRs" at the agent that
// AUTHORED those PRs. The only way it could obey is to borrow the human's
// login, which is banned.
//
// So the nudge stops and leaves the box alone. The error never carries the
// text itself, only its length: the caller is usually another agent, and a
// stranded line must reach a human, not be relayed to an agent. Retrying does
// no harm; each retry checks the box again.
var ErrNudgeInputOccupied = errors.New("nudge refused: the input box holds unsent text that gc did not type; a person must send or clear it")

const (
	// nudgeDraftEnvKey holds the start of the last message gc pasted into a
	// session. When a submit Enter is lost, gc's own paste sits in the box and
	// the queue retries the nudge. Without this, the retry would see "text in
	// the box" and refuse forever.
	nudgeDraftEnvKey = "GC_NUDGE_DRAFT_HEAD"
	// nudgeDraftHeadRunes is how much of a message is remembered. Short enough
	// to fit on the box's first line, long enough that a person is unlikely to
	// type the same words by chance.
	nudgeDraftHeadRunes = 40

	// inputProbeChar is typed to ask the box what it really holds. It cannot
	// submit (only Enter does), and it is not a digit or y/n, so a select
	// dialog ignores it. Same choice as refinery-park-detector.sh.
	inputProbeChar     = "x"
	inputProbeSettle   = 150 * time.Millisecond
	inputProbeMaxPolls = 10
)

// inputBoxVerdict is what a probe learned about text shown in the box. Only a
// ghost lets the nudge go ahead; every answer nobody planned for refuses.
type inputBoxVerdict string

const (
	boxGhost   inputBoxVerdict = "ghost"   // drawn text that is not in the buffer
	boxReal    inputBoxVerdict = "real"    // somebody's unsent typing
	boxUnknown inputBoxVerdict = "unknown" // the probe did not give a clear answer
)

// guardUnsentInput checks target's input box before a nudge types into it.
// It returns nil when the nudge may go ahead, and an error wrapping
// ErrNudgeInputOccupied when the box holds text somebody else typed.
//
// Panes with no recognizable prompt line (other TUIs, a dialog, a shell) are
// not guarded, because there is no box to read. That is the one gap.
func (t *Tmux) guardUnsentInput(target string) error {
	prefix := DefaultReadyPromptPrefix
	if configured, err := t.GetEnvironment(target, sessionReadyPromptEnvKey); err == nil {
		prefix = idlePromptPrefix(configured)
	}
	read := func() (string, bool) {
		out, err := t.run("capture-pane", "-e", "-p", "-t", target, "-S", fmt.Sprintf("-%d", promptObservationLines))
		if err != nil {
			return "", false
		}
		return inputBoxText(strings.Split(out, "\n"), prefix)
	}

	pre, found := read()
	switch {
	case !found:
		return nil
	case pre == "":
		return nil
	}
	if head, err := t.GetEnvironment(target, nudgeDraftEnvKey); err == nil && isOwnDraft(pre, head) {
		return nil
	}

	verdict := t.probeInputBox(target, pre, read)
	if verdict == boxGhost {
		return nil
	}
	return wrapOccupied(target, pre, verdict)
}

// wrapOccupied builds the refusal. It reports the box text's LENGTH, never
// the text: see ErrNudgeInputOccupied.
func wrapOccupied(target, boxText string, verdict inputBoxVerdict) error {
	return fmt.Errorf("%w (target %q, %d characters, verdict %s)", ErrNudgeInputOccupied, target, utf8.RuneCountInString(boxText), verdict)
}

// probeInputBox types one character to learn whether the text in the box is
// real, then takes the character back out. A box can SHOW text nobody typed:
// Claude re-draws a ghost of an earlier entry that looks exactly like typing.
//
//	box reads probe alone      -> the box was empty; the text was a ghost
//	box reads pre + probe      -> real text sits in the buffer
//	anything else              -> unknown (a dialog, a cut-off long line)
//
// Taking the character back out is part of the safety: a stray probe would
// ride along with the next Enter. So the restore is checked, and a probe that
// cannot be taken back reads as unknown, which refuses.
func (t *Tmux) probeInputBox(target, pre string, read func() (string, bool)) inputBoxVerdict {
	if _, err := t.run("send-keys", "-t", target, "-l", inputProbeChar); err != nil {
		return boxUnknown
	}
	probed := waitForBoxChange(read, pre)
	verdict := probeVerdict(pre, probed)

	_, _ = t.run("send-keys", "-t", target, "BSpace")
	after := waitForBoxChange(read, probed)
	// Re-send BSpace only when the box did not change at all, which means the
	// first one never landed. On any other answer a second BSpace could eat
	// one of the person's own characters.
	if after == probed && probed != pre {
		_, _ = t.run("send-keys", "-t", target, "BSpace")
		after = waitForBoxChange(read, probed)
	}

	// Only a ghost goes on to a nudge, so only a ghost's restore can let a
	// stray probe through. REAL and UNKNOWN refuse whatever the restore did.
	if verdict == boxGhost && after == inputProbeChar {
		return boxUnknown
	}
	return verdict
}

// waitForBoxChange polls the box until it reads something other than from,
// or the budget runs out. It returns the last reading.
func waitForBoxChange(read func() (string, bool), from string) string {
	last := from
	for i := 0; i < inputProbeMaxPolls; i++ {
		time.Sleep(inputProbeSettle)
		text, ok := read()
		if !ok {
			continue
		}
		last = text
		if text != from {
			return text
		}
	}
	return last
}

func probeVerdict(pre, probed string) inputBoxVerdict {
	switch probed {
	case inputProbeChar:
		return boxGhost
	case pre + inputProbeChar:
		return boxReal
	}
	return boxUnknown
}

// recordNudgeDraft remembers the start of a message gc is about to paste, so
// a retry over gc's own unsubmitted paste is not mistaken for a person's words.
func (t *Tmux) recordNudgeDraft(target, message string) error {
	return t.SetEnvironment(target, nudgeDraftEnvKey, nudgeDraftHead(message))
}

func nudgeDraftHead(message string) string {
	flat := strings.Join(strings.Fields(message), " ")
	r := []rune(flat)
	if len(r) > nudgeDraftHeadRunes {
		r = r[:nudgeDraftHeadRunes]
	}
	return string(r)
}

// isOwnDraft reports whether box text starts with gc's last paste. head is at
// most nudgeDraftHeadRunes long, so it fits on the box's first line even when
// the message itself wraps.
func isOwnDraft(box, head string) bool {
	head = strings.TrimSpace(head)
	if head == "" {
		return false
	}
	return strings.HasPrefix(strings.Join(strings.Fields(box), " "), head)
}

// inputBoxText finds the input box in a pane captured WITH color codes
// (capture-pane -e) and returns the text a person typed there. found is false
// when no line is the box.
//
// The box is the LAST prompt line that is not drawn on a background color.
// Claude draws past and queued messages with the same prompt glyph, but as a
// shaded bubble ("❯ " on a gray background). The box itself has no background.
// This matters most on a resume: for several seconds the screen shows only the
// old conversation, whose first bubble is gc's own startup prompt (first line
// "[city] rig/agent • time"). Reading that bubble as the box made the guard
// probe a box that did not exist yet, get no clear answer, and refuse every
// resume nudge (hq-51ocez). With no box on screen there is nothing anybody
// could have typed into, so there is nothing to guard.
//
// Dim text is dropped. Claude draws its placeholder ("Press up to edit queued
// messages") and its suggested next prompt dim, and real typing is never dim.
// A plain capture cannot see dim or a background, which is why this reads
// color codes.
func inputBoxText(lines []string, promptPrefix string) (text string, found bool) {
	var box styledLine
	for _, line := range lines {
		l := readStyledLine(line)
		if l.onBackground {
			continue // a message bubble, not the box
		}
		if matchesPromptPrefix(l.all, promptPrefix) {
			box, found = l, true
		}
	}
	if !found {
		return "", false
	}
	typed := strings.ReplaceAll(box.typed, "\u00a0", " ")
	typed = stripLeadingBoxBorder(typed)
	typed = strings.TrimRight(typed, " \t│┃")
	glyph := strings.TrimSpace(strings.ReplaceAll(promptPrefix, "\u00a0", " "))
	typed = strings.TrimPrefix(strings.TrimSpace(typed), glyph)
	return strings.TrimSpace(typed), true
}

// styledLine is one captured line, read with its escape codes.
type styledLine struct {
	all   string // every visible character
	typed string // only the visible characters not drawn dim
	// onBackground is true when the first visible, non-space character sits
	// on a background color: a message bubble, never the input box.
	onBackground bool
}

// sgrState is the part of the terminal's drawing style the guard cares about.
type sgrState struct {
	dim bool
	bg  bool
}

// readStyledLine reads one captured line with its escape codes.
func readStyledLine(line string) styledLine {
	var a, b strings.Builder
	var st sgrState
	var out styledLine
	sawText := false
	for i := 0; i < len(line); {
		c := line[i]
		if c != 0x1b {
			r, size := utf8.DecodeRuneInString(line[i:])
			a.WriteRune(r)
			if !st.dim {
				b.WriteRune(r)
			}
			if !sawText && r != ' ' && r != '\t' && r != '\u00a0' {
				sawText = true
				out.onBackground = st.bg
			}
			i += size
			continue
		}
		if i+1 >= len(line) {
			break
		}
		switch line[i+1] {
		case '[': // CSI: parameters, then one final byte 0x40-0x7e.
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j < len(line) && line[j] == 'm' {
				st = applySGR(line[i+2:j], st)
			}
			i = j + 1
		case ']': // OSC (for example a hyperlink): ends at BEL or ESC \.
			j := i + 2
			for j < len(line) && line[j] != 0x07 && !(line[j] == 0x1b && j+1 < len(line) && line[j+1] == '\\') {
				j++
			}
			if j < len(line) && line[j] == 0x1b {
				j++
			}
			i = j + 1
		default:
			i += 2
		}
	}
	out.all, out.typed = a.String(), b.String()
	return out
}

// applySGR returns the style after one SGR sequence's parameters.
//
//	dim: 2 turns it on; 0 (or no parameter) and 22 turn it off.
//	background: 40-47, 100-107 and 48 turn it on; 0 (or none) and 49 turn it off.
//
// Color parameters (38, 48, 58) carry numbers of their own, so "38;5;2" is a
// color, not dim.
func applySGR(params string, st sgrState) sgrState {
	if params == "" {
		return sgrState{}
	}
	fields := strings.Split(params, ";")
	for k := 0; k < len(fields); k++ {
		n, err := strconv.Atoi(fields[k])
		if err != nil {
			// Colon sub-parameters such as "48:5:237" are one color.
			if strings.HasPrefix(fields[k], "48:") {
				st.bg = true
			}
			continue
		}
		switch {
		case n == 0:
			st = sgrState{}
		case n == 22:
			st.dim = false
		case n == 2:
			st.dim = true
		case n == 49:
			st.bg = false
		case n >= 40 && n <= 47, n >= 100 && n <= 107:
			st.bg = true
		case n == 38, n == 48, n == 58:
			if n == 48 {
				st.bg = true
			}
			if k+1 < len(fields) {
				switch fields[k+1] {
				case "5":
					k += 2
				case "2":
					k += 4
				}
			}
		}
	}
	return st
}
