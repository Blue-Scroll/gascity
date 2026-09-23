import type { SessionStructuredBlock, SessionStructuredMessage } from 'gas-city-dashboard-shared';

// A message as a phone reads it. The shared transcript renderer prints every
// field a debugger could want — model, token counts, full tool input, full tool
// output — which on a 390px screen buries the two things an operator is actually
// scanning for: what the agent said, and what it is doing right now.
//
// So: prose at full width, everything else collapsed to one line behind a
// native <details>. No JS state, so a tap expands it and the page keeps working
// if anything else on it fails.

const PREVIEW = 72;

function oneLine(s: string, n = PREVIEW): string {
  const flat = s.replace(/\s+/g, ' ').trim();
  return flat.length > n ? `${flat.slice(0, n)}…` : flat;
}

function toolSummary(b: SessionStructuredBlock): string {
  const input = (b as { input?: unknown }).input;
  if (input === undefined || input === null) return '';
  if (typeof input === 'string') return oneLine(input);
  const o = input as Record<string, unknown>;
  // The field an operator recognises the call by, per tool.
  for (const k of ['command', 'file_path', 'pattern', 'path', 'query', 'prompt', 'url', 'description']) {
    const v = o[k];
    if (typeof v === 'string' && v.trim()) return oneLine(v);
  }
  return oneLine(JSON.stringify(input));
}

function resultText(b: SessionStructuredBlock): string {
  const c = (b as { content?: unknown }).content;
  if (typeof c === 'string') return c;
  if (c === undefined || c === null) return '';
  return JSON.stringify(c, null, 2);
}

const chip = 'text-label uppercase tracking-wider text-fg-faint';
const foldSummary =
  'flex min-h-11 cursor-pointer list-none items-center gap-2 text-label uppercase tracking-wider text-fg-muted marker:hidden';
const pre = 'mt-1 max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-surface-tint p-2 text-label text-fg-muted';

function Block({ block }: { block: SessionStructuredBlock }) {
  switch (block.type) {
    case 'text': {
      const text = (block as { text?: string }).text ?? '';
      if (!text.trim()) return null;
      return <p className="whitespace-pre-wrap break-words text-body text-fg">{text}</p>;
    }
    case 'thinking':
      return (
        <details>
          <summary className={foldSummary}>
            <span aria-hidden="true">▸</span> thinking
          </summary>
          <pre className={pre}>{(block as { thinking?: string }).thinking ?? '(not recorded)'}</pre>
        </details>
      );
    case 'tool_use': {
      const name = (block as { name?: string }).name ?? 'tool';
      const summary = toolSummary(block);
      return (
        <details>
          <summary className={foldSummary}>
            <span aria-hidden="true">▸</span>
            <span className="text-accent">{name}</span>
            {summary && <span className="normal-case tracking-normal text-fg-faint truncate">{summary}</span>}
          </summary>
          <pre className={pre}>{JSON.stringify((block as { input?: unknown }).input, null, 2)}</pre>
        </details>
      );
    }
    case 'tool_result': {
      const text = resultText(block);
      const n = text.length;
      return (
        <details>
          <summary className={foldSummary}>
            <span aria-hidden="true">▸</span> result
            <span className="normal-case tracking-normal text-fg-faint truncate">
              {n ? `${n.toLocaleString()} chars · ${oneLine(text, 48)}` : 'empty'}
            </span>
          </summary>
          <pre className={pre}>{text}</pre>
        </details>
      );
    }
    case 'image':
      return <p className={chip}>[image]</p>;
    default:
      return null;
  }
}

export function MessageCard({
  message,
  onCopy,
}: {
  message: SessionStructuredMessage;
  onCopy: (m: SessionStructuredMessage) => void;
}) {
  const role = message.role ?? 'unknown';
  const time = message.timestamp ? new Date(message.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '';
  const blocks = (message.blocks ?? []).filter((b) => {
    if (b.type === 'text') return ((b as { text?: string }).text ?? '').trim() !== '';
    return b.type === 'thinking' || b.type === 'tool_use' || b.type === 'tool_result' || b.type === 'image';
  });
  if (blocks.length === 0) return null;
  const mine = role === 'user';

  return (
    <article
      className={`rounded-lg px-3 py-1.5 ${mine ? 'bg-surface-tint' : ''}`}
      aria-label={`${role} message`}
    >
      <header className="mb-1 flex items-center gap-2">
        <span className={`text-label uppercase tracking-wider ${mine ? 'text-fg' : 'text-accent'}`}>
          {role}
        </span>
        {time && <span className={chip}>{time}</span>}
        <button
          type="button"
          onClick={() => onCopy(message)}
          aria-label="Copy this message"
          className="ml-auto min-h-11 px-1 text-label uppercase tracking-wider text-fg-faint hover:text-fg focus-mark"
        >
          copy
        </button>
      </header>
      <div className="space-y-1">
        {blocks.map((b, i) => (
          <Block key={i} block={b} />
        ))}
      </div>
    </article>
  );
}
