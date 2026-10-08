import type { Line, Run } from './ansi';

// Re-wrap a terminal pane at a different width.
//
// This exists because a pane has one width and the phone is not it. The agent
// laid its output out for the laptop's terminal, and the only way to show the
// same session on a 390-pixel screen without shrinking it to a smear is to lay
// it out again for that screen.
//
// That is only honest for output that is line-shaped. Measured on 400 lines of
// a live Claude Code pane: zero vertical box edges, zero corners, and a
// consistent hanging-indent structure (a bullet, then text; a continuation,
// indented under it). So the rules here are few: wrap at word boundaries,
// carry the indent onto continuation lines, redraw a horizontal rule at the
// new width, and never touch the colour. Anything boxed would come out wrong,
// and the grid view is one tap away for that.

// Terminal cell width of one code point. Wide is what a monospace font draws
// two cells for: CJK, fullwidth forms, and emoji presentation. Everything the
// TUI uses as a bullet (⏺ ❯ ⎿ ⏵) is one cell.
function wide(cp: number): boolean {
  return (
    (cp >= 0x1100 && cp <= 0x115f) ||
    (cp >= 0x2e80 && cp <= 0xa4cf) ||
    (cp >= 0xac00 && cp <= 0xd7a3) ||
    (cp >= 0xf900 && cp <= 0xfaff) ||
    (cp >= 0xfe30 && cp <= 0xfe4f) ||
    (cp >= 0xff00 && cp <= 0xff60) ||
    (cp >= 0xffe0 && cp <= 0xffe6) ||
    (cp >= 0x1f300 && cp <= 0x1f64f) ||
    (cp >= 0x1f900 && cp <= 0x1f9ff) ||
    (cp >= 0x20000 && cp <= 0x3fffd)
  );
}

export function cellWidth(s: string): number {
  let n = 0;
  for (const ch of s) {
    const cp = ch.codePointAt(0) ?? 0;
    if (cp === 0) continue;
    n += wide(cp) ? 2 : 1;
  }
  return n;
}

const RULE = /^(\s*)([─━]{4,})\s*$/;
// The prefixes Claude Code hangs text under. The indent of a continuation is
// the width of everything up to and including the space after the bullet.
const HANG = /^(\s*)(⏺|❯|⎿|└|⏵⏵|●|·|[-*•]|\d+\.)(\s+)/;

function plain(line: Line): string {
  return line.map((r) => r.text).join('');
}

function indentOf(text: string, cols: number): number {
  const hang = HANG.exec(text);
  const lead = /^\s*/.exec(text)?.[0] ?? '';
  const n = hang ? cellWidth(hang[0]) : cellWidth(lead);
  // An indent deeper than half the width leaves nothing to write in.
  return Math.min(n, Math.floor(cols / 2));
}

type Cell = { ch: string; w: number; run: number };

function cells(line: Line): Cell[] {
  const out: Cell[] = [];
  line.forEach((run, i) => {
    for (const ch of run.text) out.push({ ch, w: cellWidth(ch), run: i });
  });
  return out;
}

function runsFrom(line: Line, seg: Cell[]): Run[] {
  const out: Run[] = [];
  let current = -1;
  for (const c of seg) {
    if (c.run === current && out.length > 0) {
      out[out.length - 1]!.text += c.ch;
    } else {
      const src = line[c.run] ?? { text: '' };
      out.push({ ...src, text: c.ch });
      current = c.run;
    }
  }
  return out;
}

function wrapOne(line: Line, cols: number): Line[] {
  // A line that stands in for something (a hint rendered as a control) has no
  // characters to wrap, and rebuilding it from its characters would erase it.
  if (line.some((r) => r.hint)) return [line];
  const text = plain(line);
  const rule = RULE.exec(text);
  if (rule) {
    const lead = rule[1] ?? '';
    const glyph = (rule[2] ?? '─').charAt(0);
    const width = Math.max(4, cols - cellWidth(lead));
    const style = line.find((r) => r.text.includes(glyph)) ?? line[0] ?? { text: '' };
    return [[{ ...style, text: lead + glyph.repeat(width) }]];
  }

  const all = cells(line);
  if (all.length === 0) return [[{ text: '' }]];
  const indent = indentOf(text, cols);
  const out: Line[] = [];
  let start = 0;
  let first = true;
  while (start < all.length) {
    const limit = Math.max(1, first ? cols : cols - indent);
    let end = start;
    let width = 0;
    while (end < all.length && width + all[end]!.w <= limit) {
      width += all[end]!.w;
      end++;
    }
    if (end < all.length) {
      // Overflow: back up to the last space, unless the whole segment is one
      // word, in which case it breaks where it must.
      let b = end;
      while (b > start && all[b - 1]!.ch !== ' ') b--;
      if (b > start) end = b;
    }
    let seg = all.slice(start, end);
    while (seg.length > 0 && seg[seg.length - 1]!.ch === ' ') seg = seg.slice(0, -1);
    const runs = runsFrom(line, seg);
    if (!first && indent > 0) runs.unshift({ text: ' '.repeat(indent) });
    out.push(runs.length > 0 ? runs : [{ text: '' }]);
    start = end;
    while (start < all.length && all[start]!.ch === ' ') start++;
    first = false;
  }
  return out;
}

export function reflow(lines: Line[], cols: number): Line[] {
  const c = Math.max(8, Math.floor(cols));
  const out: Line[] = [];
  for (const line of lines) for (const l of wrapOne(line, c)) out.push(l);
  return out;
}
