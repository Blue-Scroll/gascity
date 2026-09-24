import { describe, expect, it } from 'vitest';
import { render } from '@testing-library/react';
import { ansiToReactNodes } from './ansi';

function html(text: string): string {
  const { container } = render(<>{ansiToReactNodes(text)}</>);
  return container.innerHTML;
}

describe('ansiToReactNodes', () => {
  it('keeps the text', () => {
    expect(html('plain output')).toContain('plain output');
  });

  it('gives the sixteen named colours a class the stylesheet themes', () => {
    expect(html('\u001b[32mgreen\u001b[0m')).toContain('ansi-green');
  });

  // The regression that matters: a modern TUI emits far more 256-colour than
  // named colour, and a converter that keeps only the class renders the whole
  // pane in monochrome while every test about named colours still passes.
  it('keeps 256-colour foregrounds, which have no class', () => {
    const out = html('\u001b[38;5;231mbright\u001b[39m');
    expect(out).toContain('bright');
    expect(out).toMatch(/color:\s*rgb/i);
  });

  it('keeps a 256-colour background', () => {
    expect(html('\u001b[48;5;237mshaded\u001b[49m')).toMatch(/background-color:\s*rgb/i);
  });

  it('keeps 24-bit colour', () => {
    expect(html('\u001b[38;2;10;200;30mtrue\u001b[39m')).toMatch(/color:\s*rgb/i);
  });

  // Colour is all that is carried. An agent prints arbitrary bytes, and a style
  // that could position or size an element would be a way to cover the page.
  it('carries nothing but colour out of an inline style', () => {
    const out = html('\u001b[38;5;231mx\u001b[39m');
    expect(out).not.toMatch(/position|display|width|height|z-index/i);
  });

  it('renders terminal output as elements, never as markup', () => {
    const out = html('<script>alert(1)</script>');
    expect(out).not.toContain('<script>');
    expect(out).toContain('&lt;script&gt;');
  });

  it('drops control sequences that are not colour', () => {
    const out = html('\u001b]0;a title\u0007visible');
    expect(out).toContain('visible');
    expect(out).not.toContain('a title');
  });
});
