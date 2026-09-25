import { Link } from 'react-router-dom';
import type { ListBodySessionResponse } from 'gas-city-dashboard-shared/gc-supervisor';
import { sessionRoute } from '../../lib/sessionLink';

// The front page's way into the mayor's session (vn-zh1offx). The mayor is the
// session the operator opens most, often from a phone, so it gets one button
// on Home instead of a hunt through the Agents list.

/**
 * The mayor's agent name. The button looks the session up by this name on
 * every read of the sessions list. Never save the session id instead: it
 * changes each time the mayor restarts, so a saved one becomes a dead link.
 */
export const MAYOR_AGENT = 'gastown.mayor';

/**
 * What the button can honestly say. `checking` means no answer yet: the list
 * has not arrived, or it arrived partial without the mayor in it. A failed
 * read is `unknown`, never `not-running`, because a list we could not read
 * says nothing about the mayor.
 */
export type MayorLink =
  | { kind: 'checking' }
  | { kind: 'unknown' }
  | { kind: 'not-running' }
  | { kind: 'running'; to: string };

export function mayorLink(
  list: Pick<ListBodySessionResponse, 'items' | 'partial'> | undefined,
  loading: boolean,
): MayorLink {
  if (list === undefined) return loading ? { kind: 'checking' } : { kind: 'unknown' };
  const mayors = (list.items ?? []).filter((session) => session.template === MAYOR_AGENT);
  const live = mayors
    .filter((session) => session.running && session.id !== '')
    .sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
  if (live !== undefined) {
    // The tmux name rides along so the session view can offer the live pane.
    return { kind: 'running', to: sessionRoute(live.id, '/', 'Mayor', live.session_name) };
  }
  // A running mayor with no id yet, or a partial list with no mayor row at
  // all, is still an open question. The page polls, so the answer comes.
  if (mayors.some((session) => session.running) || (list.partial === true && mayors.length === 0)) {
    return { kind: 'checking' };
  }
  return { kind: 'not-running' };
}

const SHAPE =
  'inline-flex min-h-11 shrink-0 items-center gap-2 rounded-sm border px-3 text-label uppercase tracking-wider focus-mark';

/**
 * One compact control for the page header's top right. It renders at once in
 * every state, so the page never waits on the sessions list to draw it.
 */
export function MayorButton({ link }: { link: MayorLink }) {
  if (link.kind === 'running') {
    return (
      <Link
        to={link.to}
        title="Open the live mayor session"
        className={`${SHAPE} border-rule text-fg transition-colors duration-150 ease-out-quart hover:border-accent hover:text-accent`}
      >
        <span aria-hidden="true" className="h-2 w-2 rounded-full bg-ok" />
        Mayor
      </Link>
    );
  }
  const off = OFF_STATES[link.kind];
  return (
    <button
      type="button"
      disabled
      aria-busy={link.kind === 'checking' ? true : undefined}
      title={off.title}
      className={`${SHAPE} cursor-not-allowed border-rule/60 text-fg-muted`}
    >
      <span aria-hidden="true" className={`h-2 w-2 rounded-full ${off.dot}`} />
      {off.label}
    </button>
  );
}

const OFF_STATES: Record<
  Exclude<MayorLink['kind'], 'running'>,
  { label: string; title: string; dot: string }
> = {
  checking: {
    label: 'Mayor',
    title: 'Looking for the mayor session',
    dot: 'border border-fg-faint',
  },
  unknown: {
    label: 'Mayor',
    title: 'Could not read the session list. Trying again.',
    dot: 'border border-warn',
  },
  'not-running': {
    label: 'Mayor not running',
    title: 'No mayor session is running',
    dot: 'bg-fg-faint',
  },
};
