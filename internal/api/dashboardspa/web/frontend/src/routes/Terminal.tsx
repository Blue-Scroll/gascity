import { useCallback, useRef, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { terminalFrameUrl } from '../lib/terminal';

// The terminal frame is same-origin with the dashboard, and the terminal server
// puts its xterm instance on the frame's window, so the bar above can drive the
// clipboard. That matters on a phone: a canvas terminal gets none of iOS's own
// selection and paste handles, so without this there is no way to paste a command
// into an agent or to lift text back out.
interface FrameTerm {
  paste?: (data: string) => void;
  getSelection?: () => string;
  rows?: number;
  buffer?: { active: { viewportY: number; getLine: (i: number) => { translateToString: (trim?: boolean) => string } | undefined } };
}

export function TerminalPage() {
  const { session = '' } = useParams<{ session: string }>();
  const [params] = useSearchParams();
  const back = params.get('back') ?? '/agents';
  const src = terminalFrameUrl(session);
  const frame = useRef<HTMLIFrameElement>(null);
  const [note, setNote] = useState('');

  const flash = useCallback((m: string) => {
    setNote(m);
    window.setTimeout(() => setNote(''), 1800);
  }, []);

  const term = (): FrameTerm | null => {
    try {
      return ((frame.current?.contentWindow as unknown as { term?: FrameTerm })?.term) ?? null;
    } catch {
      return null; // a cross-origin terminal is fine, it just has no clipboard help
    }
  };

  const onPaste = useCallback(async () => {
    const t = term();
    if (!t?.paste) return flash('terminal not ready');
    try {
      const text = await navigator.clipboard.readText();
      if (!text) return flash('clipboard empty');
      t.paste(text);
      flash('pasted');
    } catch {
      flash('clipboard blocked');
    }
  }, [flash]);

  // Selection first; with nothing selected, the visible screen is what the
  // operator means by "copy this" on a phone.
  const onCopy = useCallback(async () => {
    const t = term();
    if (!t) return flash('terminal not ready');
    let text = '';
    try {
      text = t.getSelection?.() ?? '';
      if (!text && t.buffer && t.rows) {
        const b = t.buffer.active;
        const lines: string[] = [];
        for (let i = 0; i < t.rows; i++) {
          lines.push(b.getLine(b.viewportY + i)?.translateToString(true) ?? '');
        }
        text = lines.join('\n').replace(/\s+$/, '');
      }
      if (!text) return flash('nothing to copy');
      await navigator.clipboard.writeText(text);
      flash('copied');
    } catch {
      flash('clipboard blocked');
    }
  }, [flash]);

  const barButton =
    'inline-flex min-h-11 items-center px-2 text-label uppercase tracking-wider text-fg-muted hover:text-fg focus-mark';

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-surface">
      <div
        className="flex shrink-0 items-center gap-1 border-b border-rule px-2"
        style={{ paddingTop: 'env(safe-area-inset-top)' }}
      >
        <Link to={back} className={barButton}>
          <span aria-hidden="true">←</span>&nbsp;Back
        </Link>
        <span className="truncate text-label uppercase tracking-wider text-fg">{session}</span>
        <span className="ml-auto flex items-center gap-1">
          {note && <span className="text-label uppercase tracking-wider text-fg-faint">{note}</span>}
          <button type="button" onClick={onCopy} className={barButton}>
            Copy
          </button>
          <button type="button" onClick={onPaste} className={barButton}>
            Paste
          </button>
        </span>
      </div>
      {src === null ? (
        <p className="p-4 text-body text-fg-muted">No terminal is configured for this dashboard.</p>
      ) : (
        <iframe
          ref={frame}
          title={`Terminal for ${session}`}
          src={src}
          className="min-h-0 w-full flex-1 border-0"
          style={{ paddingBottom: 'env(safe-area-inset-bottom)' }}
        />
      )}
    </div>
  );
}
