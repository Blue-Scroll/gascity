import type { CSSProperties, ReactNode } from 'react';
import { AnsiUp } from 'ansi_up';
import { stripTerminalControls } from './stripTerminalControls';

// Terminal output as React nodes, never as raw HTML.
//
// ansi_up produces an HTML string; we parse it and rebuild the tree ourselves
// rather than handing it to dangerouslySetInnerHTML, so nothing an agent prints
// can become markup in the dashboard. The shape ansi_up emits is small: text
// nodes, <br>, and <span> carrying either a class or an inline colour.
//
// Both matter. With use_classes the sixteen named colours come back as
// `ansi-red` and friends, which the stylesheet themes. Anything outside that
// set -- the 256-colour palette and 24-bit colour, which is most of what a
// modern TUI actually emits -- comes back as an inline style instead, so a
// converter that keeps only the class silently renders a colourful pane in
// monochrome. Colour is carried through, and only colour: the two properties
// below are an allowlist, so a crafted escape sequence cannot reach layout,
// position or anything that could cover the page.
const ALLOWED: Array<keyof CSSProperties> = ['color', 'backgroundColor'];

function styleOf(element: Element): CSSProperties | undefined {
  const raw = element.getAttribute('style');
  if (!raw) return undefined;
  const out: CSSProperties = {};
  for (const bit of raw.split(';')) {
    const at = bit.indexOf(':');
    if (at < 0) continue;
    const name = bit.slice(0, at).trim().toLowerCase();
    const value = bit.slice(at + 1).trim();
    if (!value) continue;
    const prop = name === 'color' ? 'color' : name === 'background-color' ? 'backgroundColor' : null;
    if (prop && ALLOWED.includes(prop)) out[prop] = value;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function toReact(node: ChildNode, key: string): ReactNode {
  if (node.nodeType === 3) return node.textContent ?? '';
  if (node.nodeType !== 1) return null;

  const element = node as Element;
  const children = Array.from(element.childNodes).map((child, index) => toReact(child, `${key}-${index}`));
  const tag = element.tagName.toLowerCase();
  if (tag === 'br') return <br key={key} />;
  if (tag !== 'span') return <span key={key}>{children}</span>;

  return (
    <span key={key} className={element.getAttribute('class') ?? undefined} style={styleOf(element)}>
      {children}
    </span>
  );
}

export function ansiToReactNodes(text: string): ReactNode[] {
  // Strip OSC / non-SGR CSI / lone-ESC / bare C1 control bytes before ansi_up
  // runs: it colorizes SGR and passes every other control sequence through as
  // visible text, which would leak `^[`, `\x9c` and OSC titles into the view.
  // SGR is preserved so the colour survives.
  const cleaned = stripTerminalControls(text);
  const renderer = new AnsiUp();
  renderer.use_classes = true;
  const html = renderer.ansi_to_html(cleaned);
  if (typeof DOMParser === 'undefined') return [cleaned];
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, 'text/html');
  return Array.from(doc.body.childNodes).map((node, index) => toReact(node, String(index)));
}
