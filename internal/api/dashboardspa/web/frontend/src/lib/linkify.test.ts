import { describe, expect, it } from 'vitest';
import { linkify } from './linkify';
import { reflow } from './reflow';
import type { Line } from './ansi';

const L = (...runs: Array<string | { text: string; className?: string }>): Line =>
  runs.map((r) => (typeof r === 'string' ? { text: r } : r));
const links = (line: Line) => line.filter((r) => r.link).map((r) => ({ text: r.text, ...r.link }));

describe('linkify', () => {
  it('leaves a plain line untouched', () => {
    const line = L('nothing to tap here');
    expect(linkify(line, 's')).toBe(line);
  });

  it('finds a URL and leaves trailing punctuation out of it', () => {
    expect(links(linkify(L('see https://example.test/a/b?c=1). ok'), 's'))).toEqual([
      { text: 'https://example.test/a/b?c=1', kind: 'url', href: 'https://example.test/a/b?c=1' },
    ]);
  });

  it('a path inside a URL is the URL, not a file', () => {
    const out = links(linkify(L('https://x.test/tmp/a/b.txt'), 's'));
    expect(out).toHaveLength(1);
    expect(out[0]?.kind).toBe('url');
  });

  it('an absolute path with a line citation links to the file, citation shown but not in the href', () => {
    const out = links(linkify(L('edit /Users/me/src/app.ts:42 now'), 'mayor'));
    expect(out).toEqual([
      { text: '/Users/me/src/app.ts:42', kind: 'file', href: '/file?session=mayor&path=%2FUsers%2Fme%2Fsrc%2Fapp.ts' },
    ]);
  });

  it('a home path links, and keeps its tilde for the sidecar to expand', () => {
    const out = links(linkify(L('wrote ~/.gc/dashboard-gate/Caddyfile'), 's'));
    expect(out).toEqual([
      { text: '~/.gc/dashboard-gate/Caddyfile', kind: 'file', href: '/file?session=s&path=%7E%2F.gc%2Fdashboard-gate%2FCaddyfile' },
    ]);
  });

  it('a one-segment home path is still a path', () => {
    expect(links(linkify(L('see ~/notes.txt'), 's'))[0]?.text).toBe('~/notes.txt');
  });

  it('a relative path with an extension links', () => {
    expect(links(linkify(L('in src/lib/reflow.ts:12:3'), 's'))).toEqual([
      { text: 'src/lib/reflow.ts:12:3', kind: 'file', href: '/file?session=s&path=src%2Flib%2Freflow.ts' },
    ]);
  });

  it('a slash command is not a path', () => {
    expect(links(linkify(L('try /effort high or /model'), 's'))).toEqual([]);
  });

  it('and/or and owner/repo#123 are not paths', () => {
    expect(links(linkify(L('and/or gastownhall/gascity#6515 v1.4.1'), 's'))).toEqual([]);
  });

  it('a link spanning two colour runs is tappable on both pieces, colours kept', () => {
    const line: Line = [
      { text: 'go https://a.test/', className: 'ansi-green' },
      { text: 'path/here now', className: 'ansi-red' },
    ];
    const out = linkify(line, 's');
    const pieces = out.filter((r) => r.link);
    expect(pieces.map((r) => r.text).join('')).toBe('https://a.test/path/here');
    expect(pieces.map((r) => r.className)).toEqual(['ansi-green', 'ansi-red']);
    expect(new Set(pieces.map((r) => r.link?.href)).size).toBe(1);
  });

  it('survives wrapping: both fragments of a broken URL stay linked to the same target', () => {
    const line = linkify(L('see https://example.test/a/very/long/path/that/will/not/fit'), 's');
    const wrapped = reflow([line], 24);
    expect(wrapped.length).toBeGreaterThan(1);
    const hrefs = wrapped.flatMap((l) => l.filter((r) => r.link).map((r) => r.link?.href));
    expect(hrefs.length).toBeGreaterThan(1);
    expect(new Set(hrefs).size).toBe(1);
  });
});
