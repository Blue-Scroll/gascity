import type { SessionStructuredBlock, SessionStructuredMessage } from 'gas-city-dashboard-shared';
import { keepFocus } from '../../lib/keepFocus';
import { Markdown } from '../../lib/markdown';

// A message as a phone reads it. Prose is markdown, because that is what an
// agent writes. Everything mechanical — thinking, and what a tool ran — is one
// muted line that opens on demand, so scrolling a session shows the reasoning
// rather than the machinery.

export interface ToolCall {
  id: string;
  name: string;
  command: string;
}

const PREVIEW = 64;

function oneLine(s: string, n = PREVIEW): string {
  const flat = s.replace(/\s+/g, ' ').trim();
  return flat.length > n ? `${flat.slice(0, n)}…` : flat;
}

// Every agent command here opens by moving to the city and setting the
// environment, which is true of all of them and therefore tells the reader
// nothing. Strip that so the label starts at the part that differs.
function trimPreamble(cmd: string): string {
  let out = cmd.trim();
  for (let i = 0; i < 4; i++) {
    const next = out
      .replace(/^cd\s+\S+\s*&&\s*/, '')
      .replace(/^export\s+\w+=\S*\s*;?\s*/, '')
      .replace(/^set\s+-[a-z]+\s*;?\s*/, '')
      .replace(/^(LC_ALL|LANG)=\S+\s+/, '');
    if (next === out) break;
    out = next;
  }
  return out || cmd.trim();
}

// The best label available for a call. Claude Code writes a human sentence for
// each Bash call ("Check live polecats"), but gc's structured transcript
// normalises tool input to its own fields and drops it, so the identifying
// argument is what is left. Asking upstream for that field is the real fix.
export function toolLabel(block: SessionStructuredBlock): string {
  const input = (block as { input?: unknown }).input;
  const name = (block as { name?: string }).name ?? 'tool';
  if (input && typeof input === 'object') {
    const o = input as Record<string, unknown>;
    const desc = o.description;
    if (typeof desc === 'string' && desc.trim()) return desc.trim();
    if (typeof o.command === 'string' && o.command.trim()) return oneLine(trimPreamble(o.command));
    for (const k of ['file_path', 'pattern', 'path', 'query', 'prompt', 'url']) {
      const v = o[k];
      if (typeof v === 'string' && v.trim()) return oneLine(v);
    }
  }
  if (typeof input === 'string' && input.trim()) return oneLine(input);
  return name;
}

export function toolCommand(block: SessionStructuredBlock): string {
  const input = (block as { input?: unknown }).input;
  if (typeof input === 'string') return input;
  if (input && typeof input === 'object') {
    const o = input as Record<string, unknown>;
    // A shell call reads as a command, not as JSON.
    if (typeof o.command === 'string') return o.command;
    if (typeof o.patch === 'string') return `${String(o.file_path ?? '')}\n\n${o.patch}`;
    return JSON.stringify(input, null, 2);
  }
  return '';
}

export function resultText(block: SessionStructuredBlock): string {
  const c = (block as { content?: unknown }).content;
  if (typeof c === 'string') return c;
  if (c === undefined || c === null) return '';
  return JSON.stringify(c, null, 2);
}

function messageMarkdown(m: SessionStructuredMessage): string {
  return (m.blocks ?? [])
    .filter((b) => b.type === 'text')
    .map((b) => (b as { text?: string }).text ?? '')
    .join('\n\n')
    .trim();
}

function Block({
  block,
  onOpenTool,
}: {
  block: SessionStructuredBlock;
  onOpenTool: (call: ToolCall) => void;
}) {
  switch (block.type) {
    case 'text': {
      const text = (block as { text?: string }).text ?? '';
      if (!text.trim()) return null;
      return <Markdown text={text} />;
    }
    case 'thinking':
      return (
        <details>
          <summary className="flex min-h-11 cursor-pointer list-none items-center gap-2 text-label uppercase tracking-wider text-fg-faint marker:hidden">
            <span aria-hidden="true">▸</span> thinking
          </summary>
          <pre className="mt-1 max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-surface-tint p-2 text-label text-fg-muted">
            {(block as { thinking?: string }).thinking ?? '(not recorded)'}
          </pre>
        </details>
      );
    case 'tool_use': {
      const name = (block as { name?: string }).name ?? 'tool';
      const id = (block as { id?: string }).id ?? '';
      return (
        <button
          type="button"
          onMouseDown={keepFocus}
          onClick={() => onOpenTool({ id, name, command: toolCommand(block) })}
          className="flex min-h-11 w-full items-center gap-2 text-left text-label text-fg-muted hover:text-fg focus-mark"
        >
          <span className="shrink-0 uppercase tracking-wider text-fg-faint">Ran</span>
          <span className="truncate normal-case">{toolLabel(block)}</span>
          <span aria-hidden="true" className="ml-auto shrink-0 text-fg-faint">
            ›
          </span>
        </button>
      );
    }
    // Results belong to the call that made them and are read there, so they do
    // not take a line of their own in the feed.
    case 'tool_result':
      return null;
    case 'image':
      return <p className="text-label uppercase tracking-wider text-fg-faint">[image]</p>;
    default:
      return null;
  }
}

export function MessageCard({
  message,
  onCopy,
  onOpenTool,
}: {
  message: SessionStructuredMessage;
  onCopy: (text: string, what: string) => void;
  onOpenTool: (call: ToolCall) => void;
}) {
  const role = message.role ?? 'unknown';
  const blocks = (message.blocks ?? []).filter((b) => {
    if (b.type === 'text') return ((b as { text?: string }).text ?? '').trim() !== '';
    return b.type === 'thinking' || b.type === 'tool_use' || b.type === 'image';
  });
  if (blocks.length === 0) return null;
  const mine = role === 'user';
  const prose = messageMarkdown(message);

  return (
    <article className={`rounded-lg px-3 py-1.5 ${mine ? 'bg-surface-tint' : ''}`} aria-label={`${role} message`}>
      <div className="space-y-2">
        {blocks.map((b, i) => (
          <Block key={i} block={b} onOpenTool={onOpenTool} />
        ))}
      </div>
      {/* The copy control sits under what it copies, like a message footer, and
          only where there is prose worth copying. */}
      {prose !== '' && (
        <div className="mt-1 flex items-center">
          <button
            type="button"
            onMouseDown={keepFocus}
            onClick={() => onCopy(prose, 'message')}
            aria-label="Copy this message"
            className="inline-flex h-9 w-9 items-center justify-center rounded-full text-fg-faint hover:text-fg focus-mark"
          >
            <svg viewBox="0 0 24 24" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
              <rect x="9" y="9" width="11" height="11" rx="2" />
              <path d="M5 15V5a2 2 0 0 1 2-2h10" />
            </svg>
          </button>
        </div>
      )}
    </article>
  );
}
