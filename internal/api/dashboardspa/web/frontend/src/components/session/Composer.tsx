import { useCallback, useEffect, useRef, useState } from 'react';

// Claude Code's own slash commands. The agent interprets whatever is sent, so
// this list is an affordance rather than a contract: typing a command the list
// does not know still works, and the palette only saves the typing.
const SLASH: ReadonlyArray<{ cmd: string; hint: string }> = [
  { cmd: '/clear', hint: 'start a fresh context' },
  { cmd: '/compact', hint: 'summarise and shrink the context' },
  { cmd: '/context', hint: 'show what is in the context window' },
  { cmd: '/cost', hint: 'token spend for this session' },
  { cmd: '/help', hint: 'list commands' },
  { cmd: '/model', hint: 'switch model' },
  { cmd: '/review', hint: 'review the current diff' },
  { cmd: '/status', hint: 'session and account status' },
  { cmd: '/usage', hint: 'limits and usage' },
];

const UPLOAD_URL = '/upload';

export function Composer({
  sessionId,
  onSend,
  onClose,
  onNotice,
}: {
  sessionId: string;
  onSend: (text: string) => Promise<void>;
  onClose: () => void;
  onNotice: (m: string) => void;
}) {
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const box = useRef<HTMLTextAreaElement>(null);
  const file = useRef<HTMLInputElement>(null);

  useEffect(() => {
    box.current?.focus();
  }, []);

  const grow = () => {
    const el = box.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`;
  };

  // Attachments: the bytes are written next to the city and the message carries
  // the path, which is how a coding agent takes a file anyway. Any type — an
  // image, a log, a PDF — because the agent decides what it can read, not us.
  const upload = useCallback(
    async (f: File) => {
      setBusy(true);
      try {
        const body = new FormData();
        body.append('file', f, f.name);
        body.append('session', sessionId);
        const res = await fetch(UPLOAD_URL, { method: 'POST', body, headers: { 'X-GC-Request': '1' } });
        if (res.status === 404) {
          onNotice('attachments are not set up on this machine');
          return;
        }
        if (!res.ok) {
          onNotice(`attachment failed (${res.status})`);
          return;
        }
        const out = (await res.json()) as { path?: string };
        if (!out.path) {
          onNotice('attachment failed');
          return;
        }
        setText((t) => (t ? `${t}\n${out.path}` : `${out.path}`));
        onNotice(`attached ${f.name}`);
      } catch {
        onNotice('attachment failed');
      } finally {
        setBusy(false);
      }
    },
    [sessionId, onNotice],
  );

  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const item = Array.from(e.clipboardData.files)[0];
    if (item) {
      e.preventDefault();
      void upload(item);
    }
  };

  const submit = async () => {
    const body = text.trim();
    if (!body || busy) return;
    setBusy(true);
    try {
      await onSend(body);
      setText('');
    } catch {
      onNotice('could not send');
    } finally {
      setBusy(false);
    }
  };

  const word = text.split(/\s/).pop() ?? '';
  const showSlash = word.startsWith('/') && !text.includes('\n');
  const matches = SLASH.filter((s) => s.cmd.startsWith(word));

  const btn =
    'inline-flex min-h-11 items-center px-3 text-label uppercase tracking-wider text-fg-muted hover:text-fg focus-mark';

  return (
    <div className="absolute inset-0 z-10 flex flex-col justify-end bg-fg/20" onClick={onClose}>
      <div
        className="rounded-t-xl border-t border-rule bg-surface"
        style={{ paddingBottom: 'env(safe-area-inset-bottom)' }}
        onClick={(e) => e.stopPropagation()}
      >
        {showSlash && matches.length > 0 && (
          <ul className="max-h-56 overflow-y-auto border-b border-rule">
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
        <textarea
          ref={box}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            grow();
          }}
          onPaste={onPaste}
          rows={2}
          placeholder="Message this agent. / for commands."
          className="w-full resize-none bg-transparent px-4 py-3 text-body text-fg placeholder:text-fg-faint focus:outline-none"
        />
        <div className="flex items-center gap-1 px-2 pb-2">
          <button type="button" className={btn} onClick={() => file.current?.click()} disabled={busy}>
            Attach
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
          <button type="button" className={`${btn} ml-auto`} onClick={onClose}>
            Close
          </button>
          <button
            type="button"
            className={`${btn} text-fg`}
            onClick={() => void submit()}
            disabled={busy || text.trim() === ''}
          >
            {busy ? 'Sending' : 'Send'}
          </button>
        </div>
      </div>
    </div>
  );
}
