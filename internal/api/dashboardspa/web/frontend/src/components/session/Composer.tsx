import { useEffect, useRef, useState } from 'react';
import { keepFocus } from '../../lib/keepFocus';

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

// Picked without needing an exact model id; sent as Claude Code's own /model,
// so the agent owns the vocabulary and no catalogue lives here.
const MODELS = ['default', 'opus', 'sonnet', 'haiku'] as const;

// How hard to think. Claude Code takes this as words in the message rather than
// a setting, so a choice here rides along with whatever is sent next.
const EFFORT: ReadonlyArray<{ id: string; label: string; phrase: string }> = [
  { id: 'normal', label: 'normal', phrase: '' },
  { id: 'think', label: 'think', phrase: 'think' },
  { id: 'harder', label: 'think harder', phrase: 'think harder' },
  { id: 'ultra', label: 'ultrathink', phrase: 'ultrathink' },
];

const UPLOAD_URL = '/upload';

interface Attachment {
  key: string;
  name: string;
  path: string;
  preview: string | null; // object URL, images only
}

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
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [busy, setBusy] = useState(false);
  const [sheet, setSheet] = useState(false);
  const [effort, setEffort] = useState('normal');
  const box = useRef<HTMLTextAreaElement>(null);
  const file = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 140)}px`;
  }, [text]);

  // Object URLs are only released when the chip goes away, so a preview stays
  // valid for as long as it is on screen.
  const drop = (key: string) =>
    setAttachments((list) => {
      const gone = list.find((a) => a.key === key);
      if (gone?.preview) URL.revokeObjectURL(gone.preview);
      return list.filter((a) => a.key !== key);
    });

  // Attachments: the bytes are written beside the city and the message carries
  // their paths, which is how a coding agent takes a file anyway. Any type —
  // the agent decides what it can read, not us.
  const upload = async (files: File[]) => {
    if (files.length === 0) return;
    setBusy(true);
    try {
      for (const f of files) {
        const body = new FormData();
        body.append('file', f, f.name);
        body.append('session', sessionId);
        const res = await fetch(UPLOAD_URL, {
          method: 'POST',
          body,
          headers: { 'X-GC-Request': '1' },
        });
        if (res.status === 404) {
          onNotice('attachments are not set up on this machine');
          return;
        }
        if (!res.ok) {
          onNotice(`${f.name}: attachment failed (${res.status})`);
          continue;
        }
        const out = (await res.json()) as { path?: string; name?: string };
        if (!out.path) {
          onNotice(`${f.name}: attachment failed`);
          continue;
        }
        setAttachments((list) => [
          ...list,
          {
            key: out.path as string,
            name: out.name ?? f.name,
            path: out.path as string,
            preview: f.type.startsWith('image/') ? URL.createObjectURL(f) : null,
          },
        ]);
      }
    } catch {
      onNotice('attachment failed');
    } finally {
      setBusy(false);
      box.current?.focus();
    }
  };

  const act = async (kind: 'send' | 'interrupt') => {
    if (busy) return;
    const typed = text.trim();
    const phrase = EFFORT.find((e) => e.id === effort)?.phrase ?? '';
    // Paths go in as their own lines so the agent reads them as files, and the
    // effort word rides at the end where Claude Code looks for it.
    const body = [typed, ...attachments.map((a) => a.path), phrase].filter(Boolean).join('\n');
    if (kind === 'send' && !body) return;
    setBusy(true);
    try {
      if (kind === 'send') await onSend(body);
      else await onInterrupt(body);
      setText('');
      attachments.forEach((a) => a.preview && URL.revokeObjectURL(a.preview));
      setAttachments([]);
    } catch {
      onNotice(kind === 'send' ? 'could not send' : 'could not interrupt');
    } finally {
      setBusy(false);
      // The keyboard stays up: sending one message usually means sending
      // another, and dismissing it costs a tap and the scroll position both.
      box.current?.focus();
    }
  };

  const word = text.split(/\s/).pop() ?? '';
  const showSlash = word.startsWith('/') && !text.includes('\n');
  const matches = SLASH.filter((s) => s.cmd.startsWith(word));
  const effortLabel = EFFORT.find((e) => e.id === effort)?.label ?? 'normal';
  const sendable = text.trim() !== '' || attachments.length > 0;

  const icon =
    'inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-full text-fg-muted hover:text-fg focus-mark';
  const rowButton =
    'min-h-11 rounded-full border border-rule px-3 text-label uppercase tracking-wider focus-mark';

  return (
    <div className="shrink-0 border-t border-rule bg-surface">
      {showSlash && matches.length > 0 && (
        <ul className="max-h-48 overflow-y-auto border-b border-rule">
          {matches.map((s) => (
            <li key={s.cmd}>
              <button
                type="button"
                onMouseDown={keepFocus}
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

      {sheet && (
        <div className="border-b border-rule px-3 py-2 space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-label uppercase tracking-wider text-fg-faint">model</span>
            {MODELS.map((m) => (
              <button
                key={m}
                type="button"
                onMouseDown={keepFocus}
                className={`${rowButton} text-fg-muted`}
                onClick={() => {
                  setSheet(false);
                  void onSend(`/model ${m}`);
                }}
              >
                {m}
              </button>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-label uppercase tracking-wider text-fg-faint">effort</span>
            {EFFORT.map((e) => (
              <button
                key={e.id}
                type="button"
                onMouseDown={keepFocus}
                className={`${rowButton} ${e.id === effort ? 'border-accent text-fg' : 'text-fg-muted'}`}
                onClick={() => {
                  setEffort(e.id);
                  setSheet(false);
                  box.current?.focus();
                }}
              >
                {e.label}
              </button>
            ))}
          </div>
          <p className="text-label normal-case tracking-normal text-fg-faint">
            Picking a model sends <span className="text-fg-muted">/model</span> to this agent now.
            Claude Code also saves it as this machine&apos;s default for new sessions, so the
            choice outlives this conversation. Effort rides along with the next message instead.
          </p>
        </div>
      )}

      {attachments.length > 0 && (
        <ul className="flex flex-wrap gap-2 border-b border-rule px-2 py-2">
          {attachments.map((a) => (
            <li
              key={a.key}
              className="flex items-center gap-2 rounded-lg border border-rule bg-surface-tint py-1 pl-1 pr-1"
            >
              {a.preview ? (
                <img src={a.preview} alt="" className="h-9 w-9 rounded object-cover" />
              ) : (
                <span
                  aria-hidden="true"
                  className="flex h-9 w-9 items-center justify-center rounded bg-surface text-label text-fg-muted"
                >
                  FILE
                </span>
              )}
              <span className="max-w-32 truncate text-label text-fg">{a.name}</span>
              <button
                type="button"
                onMouseDown={keepFocus}
                onClick={() => drop(a.key)}
                aria-label={`Remove ${a.name}`}
                className="inline-flex h-9 w-9 items-center justify-center rounded-full text-fg-faint hover:text-fg focus-mark"
              >
                <span aria-hidden="true">×</span>
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="flex items-end gap-1 px-1 py-1">
        <button
          type="button"
          aria-label="Attach files"
          onMouseDown={keepFocus}
          className={icon}
          onClick={() => file.current?.click()}
          disabled={busy}
        >
          <svg viewBox="0 0 24 24" className="h-6 w-6" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
            <path d="M12 5v14M5 12h14" />
          </svg>
        </button>
        <input
          ref={file}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => {
            void upload(Array.from(e.target.files ?? []));
            e.target.value = '';
          }}
        />
        <textarea
          ref={box}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onPaste={(e) => {
            // Every file on the clipboard, not just the first — pasting two
            // screenshots should attach two.
            const files = Array.from(e.clipboardData.files);
            if (files.length > 0) {
              e.preventDefault();
              void upload(files);
            }
          }}
          rows={1}
          placeholder="Message…"
          className="max-h-36 min-h-11 flex-1 resize-none bg-transparent py-2 text-body text-fg placeholder:text-fg-faint focus:outline-none"
        />
        <button
          type="button"
          aria-label="Choose model and effort"
          onMouseDown={keepFocus}
          className={`${icon} w-auto px-2 text-label uppercase tracking-wider`}
          onClick={() => setSheet((v) => !v)}
        >
          {(model ?? 'model').replace(/^claude-/, '').slice(0, 9)}
          {effort !== 'normal' ? ` · ${effortLabel}` : ''}
        </button>
        {running ? (
          <button
            type="button"
            aria-label="Stop the current run"
            onMouseDown={keepFocus}
            className={`${icon} text-warn`}
            onClick={() => void act('interrupt')}
          >
            <svg viewBox="0 0 24 24" className="h-5 w-5" fill="currentColor" aria-hidden="true">
              <rect x="6" y="6" width="12" height="12" rx="2" />
            </svg>
          </button>
        ) : (
          <button
            type="button"
            aria-label="Send"
            onMouseDown={keepFocus}
            className={`${icon} ${sendable ? 'text-fg' : 'text-fg-faint'}`}
            onClick={() => void act('send')}
            disabled={busy || !sendable}
          >
            <svg viewBox="0 0 24 24" className="h-6 w-6" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M12 19V5M5 12l7-7 7 7" />
            </svg>
          </button>
        )}
      </div>
    </div>
  );
}
