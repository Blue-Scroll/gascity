import type { ReactNode } from 'react';

// A small markdown renderer for assistant prose.
//
// It builds React nodes directly rather than HTML, so there is no
// dangerouslySetInnerHTML anywhere and nothing in a transcript can inject
// markup — which matters here, because a transcript carries tool output from
// the open internet. It covers what an agent actually writes: fenced code,
// inline code, bold, italic, links, headings, bullet and numbered lists, and
// block quotes. Anything it does not know is left as text, never swallowed.
//
// Deliberately not a dependency: the subset is small, the security story is
// "we never build HTML", and a transcript renderer is a bad place to inherit
// someone else's parser.

type Inline = ReactNode;

const CODE = /`([^`]+)`/;
const BOLD = /\*\*([^*]+)\*\*/;
const ITALIC = /(?:^|[^*])\*([^*]+)\*/;
const LINK = /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/;

function inline(text: string, key = 0): Inline[] {
  const out: Inline[] = [];
  let rest = text;
  let n = key;
  while (rest.length > 0) {
    const code = CODE.exec(rest);
    const bold = BOLD.exec(rest);
    const link = LINK.exec(rest);
    const italic = ITALIC.exec(rest);
    const candidates = [
      code && { at: code.index, len: code[0].length, node: <code key={n} className="rounded bg-surface-tint px-1 text-[0.9em]">{code[1]}</code> },
      bold && { at: bold.index, len: bold[0].length, node: <strong key={n} className="font-semibold">{bold[1]}</strong> },
      link && { at: link.index, len: link[0].length, node: <a key={n} href={link[2]} className="text-accent underline" rel="noreferrer noopener" target="_blank">{link[1]}</a> },
      // The italic pattern keeps the character before the marker, so its real
      // start is one on when that character is not the start of the string.
      italic && { at: italic.index + (italic[0].startsWith('*') ? 0 : 1), len: italic[0].length - (italic[0].startsWith('*') ? 0 : 1), node: <em key={n}>{italic[1]}</em> },
    ].filter(Boolean) as Array<{ at: number; len: number; node: ReactNode }>;
    if (candidates.length === 0) {
      out.push(rest);
      break;
    }
    const first = candidates.reduce((a, b) => (b.at < a.at ? b : a));
    if (first.at > 0) out.push(rest.slice(0, first.at));
    out.push(first.node);
    rest = rest.slice(first.at + first.len);
    n += 1;
  }
  return out;
}

export function Markdown({ text }: { text: string }) {
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  const blocks: ReactNode[] = [];
  let i = 0;
  let key = 0;

  const paragraph: string[] = [];
  const flush = () => {
    if (paragraph.length === 0) return;
    blocks.push(
      <p key={`p${key++}`} className="whitespace-pre-wrap break-words">
        {inline(paragraph.join('\n'))}
      </p>,
    );
    paragraph.length = 0;
  };

  while (i < lines.length) {
    const line = lines[i] ?? '';
    const fence = /^\s*```(\w+)?\s*$/.exec(line);
    if (fence) {
      flush();
      const body: string[] = [];
      i += 1;
      while (i < lines.length && !/^\s*```\s*$/.test(lines[i] ?? '')) {
        body.push(lines[i] ?? '');
        i += 1;
      }
      i += 1; // closing fence
      blocks.push(
        <pre
          key={`c${key++}`}
          className="overflow-x-auto rounded bg-surface-tint p-2 text-label leading-relaxed text-fg"
        >
          <code>{body.join('\n')}</code>
        </pre>,
      );
      continue;
    }
    const heading = /^(#{1,4})\s+(.*)$/.exec(line);
    if (heading) {
      flush();
      blocks.push(
        <p key={`h${key++}`} className="mt-1 font-semibold text-fg">
          {inline(heading[2] ?? '')}
        </p>,
      );
      i += 1;
      continue;
    }
    const quote = /^>\s?(.*)$/.exec(line);
    if (quote) {
      flush();
      const body = [quote[1] ?? ''];
      i += 1;
      while (i < lines.length && /^>\s?/.test(lines[i] ?? '')) {
        body.push((lines[i] ?? '').replace(/^>\s?/, ''));
        i += 1;
      }
      blocks.push(
        <blockquote key={`q${key++}`} className="border-l-2 border-rule pl-3 text-fg-muted">
          {inline(body.join('\n'))}
        </blockquote>,
      );
      continue;
    }
    const bullet = /^\s*[-*]\s+(.*)$/.exec(line);
    const numbered = /^\s*(\d+)[.)]\s+(.*)$/.exec(line);
    if (bullet || numbered) {
      flush();
      const ordered = !bullet;
      const items: string[] = [];
      while (i < lines.length) {
        const b = /^\s*[-*]\s+(.*)$/.exec(lines[i] ?? '');
        const nmb = /^\s*(\d+)[.)]\s+(.*)$/.exec(lines[i] ?? '');
        if (ordered && nmb) items.push(nmb[2] ?? '');
        else if (!ordered && b) items.push(b[1] ?? '');
        else break;
        i += 1;
      }
      const Tag = ordered ? 'ol' : 'ul';
      blocks.push(
        <Tag
          key={`l${key++}`}
          className={`ml-4 space-y-0.5 ${ordered ? 'list-decimal' : 'list-disc'}`}
        >
          {items.map((it, n) => (
            <li key={n} className="break-words">
              {inline(it)}
            </li>
          ))}
        </Tag>,
      );
      continue;
    }
    if (line.trim() === '') {
      flush();
      i += 1;
      continue;
    }
    paragraph.push(line);
    i += 1;
  }
  flush();

  return <div className="space-y-2 text-body text-fg">{blocks}</div>;
}
