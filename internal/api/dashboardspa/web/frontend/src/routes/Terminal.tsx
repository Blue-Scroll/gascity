import { Link, useParams, useSearchParams } from 'react-router-dom';
import { terminalFrameUrl } from '../lib/terminal';

// A full-bleed terminal with a back bar above it. Deliberately not inside the
// page Layout: the terminal wants the whole viewport, and on a phone saved to
// the home screen there is no browser chrome to navigate with, so the bar is
// the only way back and must always be on screen.
export function TerminalPage() {
  const { session = '' } = useParams<{ session: string }>();
  const [params] = useSearchParams();
  const back = params.get('back') ?? '/agents';
  const src = terminalFrameUrl(session);

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-surface">
      <div
        className="flex shrink-0 items-center gap-2 border-b border-rule px-2"
        style={{ paddingTop: 'env(safe-area-inset-top)' }}
      >
        <Link
          to={back}
          className="inline-flex min-h-11 items-center gap-1 px-2 text-label uppercase tracking-wider text-fg-muted hover:text-fg focus-mark"
        >
          <span aria-hidden="true">←</span> Back
        </Link>
        <span className="truncate text-label uppercase tracking-wider text-fg">{session}</span>
      </div>
      {src === null ? (
        <p className="p-4 text-body text-fg-muted">
          No terminal is configured for this dashboard.
        </p>
      ) : (
        <iframe
          title={`Terminal for ${session}`}
          src={src}
          className="min-h-0 w-full flex-1 border-0"
          style={{ paddingBottom: 'env(safe-area-inset-bottom)' }}
        />
      )}
    </div>
  );
}
