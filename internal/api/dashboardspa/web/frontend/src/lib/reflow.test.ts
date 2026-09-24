import { describe, expect, it } from 'vitest';
import { cellWidth, reflow } from './reflow';
import type { Line } from './ansi';

const L = (...runs: Array<string | { text: string; className?: string }>): Line =>
  runs.map((r) => (typeof r === 'string' ? { text: r } : r));
const text = (lines: Line[]) => lines.map((l) => l.map((r) => r.text).join(''));

describe('reflow', () => {
  it('leaves a short line alone', () => {
    expect(text(reflow([L('short')], 40))).toEqual(['short']);
  });

  it('wraps at a word boundary, never mid-word when it can help it', () => {
    const out = text(reflow([L('the quick brown fox jumps over the lazy dog')], 16));
    expect(out).toEqual(['the quick brown', 'fox jumps over', 'the lazy dog']);
    for (const l of out) expect(cellWidth(l)).toBeLessThanOrEqual(16);
  });

  it('hangs continuation lines under a bullet, the way the TUI does', () => {
    const out = text(reflow([L('⏺ Read the file and found three problems worth fixing today')], 24));
    expect(out[0]).toBe('⏺ Read the file and');
    expect(out[1]).toBe('  found three problems');
    expect(out[2]).toBe('  worth fixing today');
  });

  it('hangs under the tool-result marker at its own depth', () => {
    const out = text(reflow([L('  ⎿  Listed 12 files in the directory tree')], 24));
    expect(out[0]).toBe('  ⎿  Listed 12 files in');
    expect(out[1]).toBe('     the directory tree');
  });

  it('keeps a plain indent on continuation lines', () => {
    const out = text(reflow([L('      indented prose that runs on and on past the edge')], 24));
    expect(out[0]).toBe('      indented prose');
    expect(out[1]).toBe('      that runs on and');
  });

  it('redraws a horizontal rule at the new width instead of wrapping it', () => {
    const out = text(reflow([L('─'.repeat(160))], 40));
    expect(out).toEqual(['─'.repeat(40)]);
  });

  it('keeps colour on both sides of a wrap', () => {
    const line: Line = [
      { text: 'plain then ' },
      { text: 'a red run that is long enough to wrap', className: 'ansi-red' },
    ];
    const out = reflow([line], 20);
    expect(out.length).toBeGreaterThan(1);
    const redOn = out.map((l) => l.some((r) => r.className === 'ansi-red' && r.text.length > 0));
    expect(redOn.filter(Boolean).length).toBeGreaterThan(1);
    // and nothing that was plain became red
    expect(out[0]?.[0]?.className).toBeUndefined();
    expect(out[0]?.[0]?.text).toMatch(/^plain then/);
  });

  it('breaks a word longer than the line where it must', () => {
    const out = text(reflow([L('x'.repeat(30))], 12));
    expect(out).toEqual(['x'.repeat(12), 'x'.repeat(12), 'x'.repeat(6)]);
  });

  it('keeps an empty line as one empty line', () => {
    expect(text(reflow([L('')], 40))).toEqual(['']);
  });

  it('counts a wide glyph as two cells', () => {
    expect(cellWidth('日本')).toBe(4);
    expect(cellWidth('⏺ ok')).toBe(4);
  });

  it('passes a hint line through untouched, since it has no characters to wrap', () => {
    const hint: Line = [{ text: '', hint: 'reply' }];
    expect(reflow([hint], 40)).toEqual([hint]);
  });

  it('never lets an indent eat the whole line', () => {
    const out = text(reflow([L(' '.repeat(30) + 'deep text here')], 20));
    for (const l of out) expect(cellWidth(l)).toBeLessThanOrEqual(20);
    expect(out.join(' ')).toContain('deep');
  });
});
