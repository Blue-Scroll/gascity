import type { Line } from './ansi';

// Claude Code draws its own input box at the bottom of the pane: a rule, a
// line beginning with the prompt glyph, whatever has been typed so far, and
// a rule to close it. On the laptop that box is where you type. On the phone
// it is not -- the message box below the pane is -- and a prompt you cannot
// type into, sometimes holding half a sentence someone typed on the laptop,
// is the one part of the pane that misleads. So the view replaces the box
// with a hint that focuses the real input when tapped.
//
// The status line under the box stays. It carries the permission mode, the
// busy indicator and the context figure, all of which are worth reading.
//
// Recognition is by shape, not by position: the last prompt line that has a
// rule directly above it and a rule within a few lines below it. A prompt
// glyph anywhere else, or one without its rules, is left alone.
const RULE = /^\s*[─━]{8,}\s*$/;
const PROMPT = /^\s*[❯>]\s?/;
const BELOW = 12; // a typed prompt can wrap onto several lines before the closing rule

function plain(line: Line): string {
  return line.map((r) => r.text).join('');
}

export type Hidden = { lines: Line[]; hidden: boolean };

export function hideInputBox(lines: Line[]): Hidden {
  for (let i = lines.length - 1; i >= 1; i--) {
    const text = plain(lines[i] ?? []);
    if (!PROMPT.test(text) || !text.trimStart().startsWith('❯')) continue;
    if (!RULE.test(plain(lines[i - 1] ?? []))) return { lines, hidden: false };
    let close = -1;
    for (let j = i + 1; j < lines.length && j <= i + BELOW; j++) {
      if (RULE.test(plain(lines[j] ?? []))) {
        close = j;
        break;
      }
    }
    if (close < 0) return { lines, hidden: false };
    const out = [...lines.slice(0, i - 1), [{ text: '', hint: 'reply' as const }], ...lines.slice(close + 1)];
    return { lines: out, hidden: true };
  }
  return { lines, hidden: false };
}
