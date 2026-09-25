import { useEffect, useRef, useState } from 'react';
import { matchCommands, useSlashCommands } from '../../lib/commands';
import { keepFocus } from '../../lib/keepFocus';
import { useUploadsServed, writeAttachment } from '../../lib/upload';

// Sent as Claude Code's own /model, so the agent owns the vocabulary. The
// short names are aliases Claude Code resolves itself; fable has no alias yet,
// so it goes by its full id.
const MODELS: ReadonlyArray<{ id: string; label: string }> = [
  { id: 'default', label: 'default' },
  { id: 'claude-fable-5-1', label: 'fable' },
  { id: 'opus', label: 'opus' },
  { id: 'sonnet', label: 'sonnet' },
  { id: 'haiku', label: 'haiku' },
];

// How hard to think. Claude Code takes this as words in the message rather than
// a setting, so a choice here rides along with whatever is sent next.
const EFFORT: ReadonlyArray<{ id: string; label: string; phrase: string }> = [
  { id: 'normal', label: 'normal', phrase: '' },
  { id: 'think', label: 'think', phrase: 'think' },
  { id: 'harder', label: 'think harder', phrase: 'think harder' },
  { id: 'ultra', label: 'ultrathink', phrase: 'ultrathink' },
];

// An attachment is held in the browser until the message is actually sent.
// Nothing reaches the machine's disk for a file that is attached and then
// removed, or typed alongside and then abandoned.
interface Attachment {
  key: string;
  name: string;
  file: File;
  preview: string | null; // object URL, images only
}

let attachSeq = 0;

export function Composer({
  sessionId,
  tmuxSession,
  running,
  model,
  onSend,
  onInterrupt,
  onNotice,
}: {
  sessionId: string;
  // The agent's tmux session name, or '' when the link did not carry one. It is
  // how the "/" list finds this agent's own skills; with '' the list is the
  // built-in commands alone.
  tmuxSession: string;
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
  // Attaching needs the machine's upload helper. Where it is not served, there
  // is no attach button and a pasted file is not picked up, so nothing is
  // offered that could only fail at send.
  const canAttach = useUploadsServed() === true;
  const commands = useSlashCommands(tmuxSession);

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

  // Picking or pasting only stages the file in the browser.
  const stage = (files: File[]) => {
    if (files.length === 0) return;
    setAttachments((list) => [
      ...list,
      ...files.map((f) => ({
        key: `a${(attachSeq += 1)}`,
        name: f.name || 'file',
        file: f,
        preview: f.type.startsWith('image/') ? URL.createObjectURL(f) : null,
      })),
    ]);
    box.current?.focus();
  };

  // The write happens here, at send, and only for what is still attached. An
  // agent reads a file from the filesystem by path, so the bytes have to land
  // somewhere it can see; they land in a swept directory with a short life
  // rather than a permanent one.
  const writeAttachments = async (list: Attachment[]): Promise<string[] | null> => {
    const paths: string[] = [];
    for (const a of list) {
      const written = await writeAttachment(sessionId, a.file, a.name);
      if ('problem' in written) {
        onNotice(written.problem);
        return null;
      }
      paths.push(written.path);
    }
    return paths;
  };

  const act = async (kind: 'send' | 'interrupt') => {
    if (busy) return;
    const typed = text.trim();
    const staged = attachments;
    const phrase = EFFORT.find((e) => e.id === effort)?.phrase ?? '';
    if (kind === 'send' && !typed && staged.length === 0) return;
    setBusy(true);
    // Clear the moment the operator commits, not when the network agrees:
    // waiting means their words sit in the box through the whole round trip and
    // anything they type meanwhile is wiped when the clear finally lands.
    setText('');
    setAttachments([]);
    try {
      const paths = staged.length > 0 ? await writeAttachments(staged) : [];
      if (paths === null) throw new Error('attachment failed');
      // Paths go in as their own lines so the agent reads them as files, and the
      // effort word rides at the end where Claude Code looks for it.
      const body = [typed, ...paths, phrase].filter(Boolean).join('\n');
      if (kind === 'send') await onSend(body);
      else await onInterrupt(body);
      staged.forEach((a) => a.preview && URL.revokeObjectURL(a.preview));
    } catch {
      // Nothing was delivered, so give the operator their message back exactly
      // as they had it rather than make them retype it.
      setText(typed);
      setAttachments(staged);
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
  const matches = showSlash ? matchCommands(commands, word) : [];
  const effortLabel = EFFORT.find((e) => e.id === effort)?.label ?? 'normal';
  const sendable = text.trim() !== '' || attachments.length > 0;

  const icon =
    'inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-full text-fg-muted hover:text-fg focus-mark';
  const rowButton =
    'min-h-11 rounded-full border border-rule px-3 text-label uppercase tracking-wider focus-mark';

  return (
    <div className="shrink-0 border-t border-rule bg-surface">
      {matches.length > 0 && (
        <ul aria-label="Commands" className="max-h-60 overflow-y-auto border-b border-rule">
          {matches.map((c) => (
            <li key={c.name}>
              <button
                type="button"
                onMouseDown={keepFocus}
                className="flex min-h-11 w-full min-w-0 flex-col justify-center px-4 py-1 text-left focus-mark"
                onClick={() => {
                  setText((t) => `${t.slice(0, t.length - word.length)}/${c.name} `);
                  box.current?.focus();
                }}
              >
                <span className="text-body text-fg">/{c.name}</span>
                {/* Skill descriptions run long, so one line of it, and where
                    the command comes from, the way the pane tags its rows. */}
                <span className="w-full truncate text-label text-fg-faint">
                  {c.source !== 'builtin' && (
                    <span className="uppercase tracking-wider">{c.source} · </span>
                  )}
                  {c.description}
                </span>
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
                key={m.id}
                type="button"
                onMouseDown={keepFocus}
                className={`${rowButton} text-fg-muted`}
                onClick={() => {
                  setSheet(false);
                  void onSend(`/model ${m.id}`);
                }}
              >
                {m.label}
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

      {/* The message box has a row to itself, the full width of the screen. On a
          phone every button beside it takes width from what is being typed, so
          the buttons sit on the row below: attach and model on the left, stop
          and send on the right, where a thumb finds send. */}
      <div className="px-3 pt-1">
        <textarea
          data-composer-input
          ref={box}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onPaste={(e) => {
            // Every file on the clipboard, not just the first — pasting two
            // screenshots should attach two.
            const files = canAttach ? Array.from(e.clipboardData.files) : [];
            if (files.length > 0) {
              e.preventDefault();
              stage(files);
            }
          }}
          rows={1}
          placeholder="Message…"
          className="block max-h-36 min-h-11 w-full resize-none bg-transparent py-2 text-body text-fg placeholder:text-fg-faint focus:outline-none"
        />
      </div>

      <div aria-label="Message actions" role="group" className="flex items-center gap-1 px-1 pb-1">
        {canAttach && (
          <>
            <button
              type="button"
              aria-label="Attach files"
              onMouseDown={keepFocus}
              className={icon}
              onClick={() => file.current?.click()}
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
                stage(Array.from(e.target.files ?? []));
                e.target.value = '';
              }}
            />
          </>
        )}
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
        <div className="ml-auto flex items-center gap-1">
          {/* Stop is its own control, not a replacement for send: it is drawn as
              a square in a ring so it reads as "stop", and it only appears while
              there is a run to stop. Send keeps its arrow either way. */}
          {running && (
            <button
              type="button"
              aria-label="Stop the current run"
              title="Interrupt the run with whatever is typed"
              onMouseDown={keepFocus}
              className={`${icon} text-warn`}
              onClick={() => void act('interrupt')}
            >
              <svg viewBox="0 0 24 24" className="h-6 w-6" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
                <circle cx="12" cy="12" r="9" />
                <rect x="9" y="9" width="6" height="6" rx="1" fill="currentColor" stroke="none" />
              </svg>
            </button>
          )}
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
        </div>
      </div>
    </div>
  );
}
