import type { Line, Link, Run } from './ansi';

// Find what a person would want to tap in a line of terminal output.
//
// The pane is text, so nothing in it is a link until something says so. Two
// kinds are worth saying: a URL, which opens where it points, and a file path,
// which opens the file through the sidecar so a screenshot the agent took or a
// file it wrote can be looked at on the phone. Paths are the useful case and
// the risky one for false positives, so the rules are narrow: an absolute or
// home path needs two segments (`/effort` is a slash command, `/tmp/x` is a
// path), and a relative path needs a slash and an extension (`src/lib/a.ts`
// yes, `and/or` no, `owner/repo#12` no).
//
// This runs on the line as the agent wrote it, before wrapping, and marks the
// runs. The wrap step copies run attributes onto every fragment, so a URL that
// breaks across two visual lines is tappable on both.
const URL_RE = /https?:\/\/[^\s<>"'`]+/gu;
// The left boundary matters: without it, `/lib/a.ts` inside `src/lib/a.ts` reads
// as an absolute path and the relative one is never seen.
// `~/x` is a path on its own; `/x` needs a second segment so a slash command
// is not one.
const ABS_RE =
  /(?<![\w.~-])(?:~\/[\w.@+%-]+(?:\/[\w.@+%-]+)*|\/[\w.@+%-]+(?:\/[\w.@+%-]+)+)(?::\d+){0,2}/g;
const REL_RE = /(?<![\w./~-])[\w.-]+(?:\/[\w.-]+)+\.[A-Za-z0-9]{1,8}(?::\d+){0,2}/g;
const TRAIL = /[.,;:!?)\]}'"`>]+$/;
const LINE_CITE = /(?::\d+){1,2}$/;

type Span = { start: number; end: number; link: Link };

export function fileHref(session: string, path: string): string {
  const q = new URLSearchParams({ session, path });
  return `/file?${q.toString()}`;
}

function spansOf(text: string, session: string): Span[] {
  const out: Span[] = [];
  const taken: Array<[number, number]> = [];
  const free = (a: number, b: number) => taken.every(([x, y]) => b <= x || a >= y);

  for (const m of text.matchAll(URL_RE)) {
    const raw = m[0];
    const trimmed = raw.replace(TRAIL, '');
    if (!trimmed) continue;
    const start = m.index ?? 0;
    out.push({ start, end: start + trimmed.length, link: { kind: 'url', href: trimmed } });
    taken.push([start, start + trimmed.length]);
  }
  for (const re of [ABS_RE, REL_RE]) {
    for (const m of text.matchAll(re)) {
      const raw = m[0].replace(TRAIL, '');
      if (!raw) continue;
      const start = m.index ?? 0;
      const end = start + raw.length;
      if (!free(start, end)) continue;
      const path = raw.replace(LINE_CITE, '');
      out.push({ start, end, link: { kind: 'file', href: fileHref(session, path) } });
      taken.push([start, end]);
    }
  }
  return out.sort((a, b) => a.start - b.start);
}

// Split the runs at span edges, attaching the link to the pieces inside one.
export function linkify(line: Line, session: string): Line {
  const text = line.map((r) => r.text).join('');
  const spans = spansOf(text, session);
  if (spans.length === 0) return line;
  const out: Run[] = [];
  let pos = 0;
  for (const run of line) {
    const a = pos;
    const b = pos + run.text.length;
    let cursor = a;
    for (const sp of spans) {
      if (sp.end <= cursor || sp.start >= b) continue;
      const s = Math.max(sp.start, cursor);
      const e = Math.min(sp.end, b);
      if (s > cursor) out.push({ ...run, text: text.slice(cursor, s) });
      out.push({ ...run, text: text.slice(s, e), link: sp.link });
      cursor = e;
    }
    if (cursor < b) out.push({ ...run, text: text.slice(cursor, b) });
    pos = b;
  }
  return out;
}
