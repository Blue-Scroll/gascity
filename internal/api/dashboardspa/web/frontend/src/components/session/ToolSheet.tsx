import { keepFocus } from '../../lib/keepFocus';

// What a tool call actually did, on demand. The transcript line stays short —
// an operator scrolling a session wants to know that something ran, not to read
// a screen of JSON — and this is where the detail lives when they ask for it.
export function ToolSheet({
  name,
  command,
  output,
  onClose,
  onCopy,
}: {
  name: string;
  command: string;
  output: string | null;
  onClose: () => void;
  onCopy: (text: string, what: string) => void;
}) {
  const section = 'mt-1 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-surface-tint p-2 text-label leading-relaxed text-fg';
  const head = 'flex items-center gap-2 text-label uppercase tracking-wider text-fg-muted';

  return (
    <div className="absolute inset-0 z-20 flex flex-col justify-end bg-fg/20" onClick={onClose}>
      <div
        className="max-h-[85%] overflow-y-auto rounded-t-xl border-t border-rule bg-surface"
        style={{ paddingBottom: 'env(safe-area-inset-bottom)' }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="sticky top-0 flex items-center gap-2 border-b border-rule bg-surface px-2 py-2">
          <button
            type="button"
            onMouseDown={keepFocus}
            onClick={onClose}
            aria-label="Close"
            className="inline-flex h-11 w-11 items-center justify-center rounded-full text-fg-muted hover:text-fg focus-mark"
          >
            <svg viewBox="0 0 24 24" className="h-5 w-5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
          <span className="text-title font-semibold text-fg">{name}</span>
        </div>
        <div className="space-y-3 px-3 py-3">
          <div>
            <div className={head}>
              Command
              <button
                type="button"
                onMouseDown={keepFocus}
                onClick={() => onCopy(command, 'command')}
                className="ml-auto min-h-11 px-2 text-label uppercase tracking-wider text-fg-faint hover:text-fg focus-mark"
              >
                copy
              </button>
            </div>
            <pre className={section}>{command}</pre>
          </div>
          <div>
            <div className={head}>
              Output
              {output && (
                <button
                  type="button"
                  onMouseDown={keepFocus}
                  onClick={() => onCopy(output, 'output')}
                  className="ml-auto min-h-11 px-2 text-label uppercase tracking-wider text-fg-faint hover:text-fg focus-mark"
                >
                  copy
                </button>
              )}
            </div>
            <pre className={section}>{output ?? 'Still running, or nothing was returned.'}</pre>
          </div>
        </div>
      </div>
    </div>
  );
}
