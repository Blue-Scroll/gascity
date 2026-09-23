import { useEffect, useRef, useState } from 'react';

// Claude Code's own slash commands. The agent interprets whatever is sent, so
// this list is an affordance rather than a contract: a command it does not know
// still works, the palette only saves the typing.
const SLASH: ReadonlyArray<{ cmd: string; hint: string }> = [
  { cmd: '/clear', hint: 'start a fresh context' },
  { cmd: '/compact', hint: 'summarise and shrink the context' },
  { cmd: '/context', hint: 'what is in the context window' },
  { cmd: '/cost', hint: 'token spend for this session' },
  { cmd: '/help', hint: 'list commands' },
  { cmd: '/model', hint: 'switch model' },
  { cmd: '/review', hint: 'review the current diff' },
  { cmd: '/status', hint: 'session and account status' },
  { cmd: '/usage', hint: 'limits and usage' },
];

// Whatever the operator can pick without knowing an exact model id. Sent as
// Claude Code's own /model command, so the agent owns the vocabulary and this
// list never has to track a model catalogue.
const MODELS = ['default', 'opus', 'sonnet', 'haiku'] as const;

const UPLOAD_URL = '/upload';

export function Composer({
  sessionId,
  running,
  model,
  onSend,
  onInterrupt,
  onNotice,
}: {
  sessionId: string;
  running: boolean;
  model: string | null;
  onSend: (text: string) => Promise<void>;
  onInterrupt: (text: string) => Promise<void>;
  onNotice: (m: string) => void;
}) {
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [models, setModels] = useState(false);
  const box = useRef<HTMLTextAreaElement>(null);
  const file = useRef<HTMLInputElement>(null);

  const grow = () => {
    const el = box.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 140)}px`;
  };
  useEffect(grow, [text]);

  // Attachments: the bytes are written beside the city and the message carries
  // the path, which is how a coding agent takes a file anyway. Any type — the
  // agent decides what it can read, not us.
  const upload = async (f: File) => {
    setBusy(true);
    try {
      const body = new FormData();
      body.append('file', f, f.name);
      body.append('session', sessionId);
      const res = await fetch(UPLOAD_URL, { method: 'POST', body, headers: { 'X-GC-Request': '1' } });
      if (res.status === 404) return onNotice('attachments are not set up on this machine');
      if (!res.ok) return onNotice(`attachment failed (${res.status})`);
      const out = (await res.json()) as { path?: string; name?: string };
      const stored = out.path;
      if (!stored) return onNotice('attachment failed');
      setText((t) => (t ? `${t}\n${stored}` : stored));
      onNotice(`attached ${out.name ?? f.name}`);
    } catch {
      onNotice('attachment failed');
    } finally {
      setBusy(false);
      box.current?.focus();
    }
  };

  const act = async (kind: 'send' | 'interrupt') => {
    const body = text.trim();
    if (busy) return;
    if (kind === 'send' && !body) return;
    setBusy(true);
    try {
      if (kind === 'send') await onSend(body);
      else await onInterrupt(body);
      setText('');
    } catch {
      onNotice(kind === 'send' ? 'could not send' : 'could not interrupt');
    } finally {
      setBusy(false);
      // The keyboard stays up: sending one message usually means sending
      // another, and on a phone dismissing it costs a tap and the scroll
      // position both.
      box.current?.focus();
    }
  };

  const word = text.split(/\s/).pop() ?? '';
  const showSlash = word.startsWith('/') && !text.includes('\n');
  const matches = SLASH.filter((s) => s.cmd.startsWith(word));

  const icon =
    'inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-full text-fg-muted hover:text-fg focus-mark';

  return (
    <div className="shrink-0 border-t border-rule bg-surface">
      {showSlash && matches.length > 0 && (
        <ul className="max-h-48 overflow-y-auto border-b border-rule">
          {matches.map((s) => (
            <li key={s.cmd}>
              <button
                type="button"
                className="flex min-h-11 w-full items-baseline gap-3 px-4 text-left focus-mark"
                onClick={() => {
                  setText((t) => `${t.slice(0, t.length - word.length)}${s.cmd} `);
                  box.current?.focus();
                }}
              >
                <span className="text-body text-fg">{s.cmd}</span>
                <span className="text-label uppercase tracking-wider text-fg-faint">{s.hint}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {models && (
        <ul className="flex flex-wrap gap-1 border-b border-rule px-2 py-1">
          {MODELS.map((m) => (
            <li key={m}>
              <button
                type="button"
                className="min-h-11 px-3 text-label uppercase tracking-wider text-fg-muted focus-mark"
                onClick={() => {
                  setModels(false);
                  void onSend(`/model ${m}`);
                }}
              >
                {m}
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex items-end gap-1 px-1 py-1">
        <button type="button" aria-label="Attach a file" className={icon} onClick={() => file.current?.click()} disabled={busy}>
          <span className="text-title leading-none">+</span>
        </button>
        <input
          ref={file}
          type="file"
          className="hidden"
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) void upload(f);
            e.target.value = '';
          }}
        />
        <textarea
          ref={box}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onPaste={(e) => {
            const f = Array.from(e.clipboardData.files)[0];
            if (f) {
              e.preventDefault();
              void upload(f);
            }
          }}
          rows={1}
          placeholder="Message…"
          className="max-h-36 min-h-11 flex-1 resize-none bg-transparent py-2 text-body text-fg placeholder:text-fg-faint focus:outline-none"
        />
        <button
          type="button"
          aria-label="Choose a model"
          className={`${icon} w-auto px-2 text-label uppercase tracking-wider`}
          onClick={() => setModels((v) => !v)}
        >
          {(model ?? 'model').replace(/^claude-/, '').slice(0, 10)}
        </button>
        {running ? (
          <button
            type="button"
            aria-label="Stop the current run"
            className={`${icon} text-warn`}
            onClick={() => void act('interrupt')}
          >
            <span aria-hidden="true">◼</span>
          </button>
        ) : (
          <button
            type="button"
            aria-label="Send"
            className={`${icon} ${text.trim() ? 'text-fg' : 'text-fg-faint'}`}
            onClick={() => void act('send')}
            disabled={busy || !text.trim()}
          >
            <span aria-hidden="true">↑</span>
          </button>
        )}
      </div>
    </div>
  );
}
