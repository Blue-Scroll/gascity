import { describe, expect, it } from 'vitest';
import { hideInputBox } from './inputbox';
import type { Line } from './ansi';

const L = (s: string): Line => [{ text: s }];
const rule = L('─'.repeat(40));
const status = L('  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt');
const text = (lines: Line[]) => lines.map((l) => l.map((r) => r.text).join(''));

describe('hideInputBox', () => {
  it('replaces the box with a hint and keeps the status line', () => {
    const out = hideInputBox([L('⏺ done'), rule, L('❯ '), rule, status]);
    expect(out.hidden).toBe(true);
    expect(text(out.lines)).toEqual(['⏺ done', '', '  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt']);
    expect(out.lines[1]?.[0]?.hint).toBe('reply');
  });

  it('hides whatever was typed into the box, including a wrapped prompt', () => {
    const out = hideInputBox([rule, L('❯ a long prompt that has'), L('  wrapped onto a second line'), rule, status]);
    expect(out.hidden).toBe(true);
    expect(text(out.lines).join('\n')).not.toContain('long prompt');
  });

  it('recognises the prompt as Claude Code actually draws it: glyph, then a no-break space', () => {
    const out = hideInputBox([rule, L('\u276f\u00a0'), rule, status]);
    expect(out.hidden).toBe(true);
  });

  it('leaves a pane with no box alone', () => {
    const lines = [L('⏺ working…'), L('  ⎿ output')];
    expect(hideInputBox(lines)).toEqual({ lines, hidden: false });
  });

  it('leaves a prompt glyph that has no rules around it alone', () => {
    const lines = [L('the prompt looks like ❯ in the docs'), L('❯ quoted')];
    expect(hideInputBox(lines).hidden).toBe(false);
  });

  it('a rule far below is not this box', () => {
    const lines = [rule, L('❯ '), ...Array.from({ length: 20 }, (_, i) => L(`line ${i}`)), rule];
    expect(hideInputBox(lines).hidden).toBe(false);
  });

  it('uses the last box, not an earlier one', () => {
    const out = hideInputBox([rule, L('❯ old'), rule, L('⏺ answer'), rule, L('❯ '), rule, status]);
    expect(out.hidden).toBe(true);
    // the earlier, already-submitted box is history and stays as it was drawn
    expect(text(out.lines)[1]).toBe('❯ old');
    expect(text(out.lines)).toContain('⏺ answer');
  });

  it('keeps a menu drawn under the box', () => {
    const out = hideInputBox([rule, L('❯ /eff'), rule, L('  /effort  set thinking effort'), status]);
    expect(text(out.lines)).toContain('  /effort  set thinking effort');
  });
});
