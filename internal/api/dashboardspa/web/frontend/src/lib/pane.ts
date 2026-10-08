import { useServed } from './served';

// The agent's live tmux pane, read straight from the machine it runs on.
//
// The transcript is something the city assembles from messages. A pane is what
// the agent is actually drawing -- its boxes, its permission banner, its
// spinner, its prompt -- and none of that exists as a message. When you want to
// see what an agent is doing rather than what it said, only the pane will do.
//
// Served by the dashboard's local sidecar behind the same gate as everything
// else, so it is present only where an operator has set that up. With no
// sidecar the pane view is simply absent and the transcript is the whole story.
const PANE = '/pane';
const KEYS = '/keys';

export type Pane = {
  text: string;
  start: number;
  end: number;
  before: number;
  history: number;
  height: number;
  width: number;
  at_oldest: boolean;
};

// Named keys the sidecar will send. Text goes through the session API as it
// always has; these are the ones a text box cannot express, which is exactly
// why a TUI needs them.
export type PaneKey =
  | 'escape'
  | 'enter'
  | 'tab'
  | 'btab'
  | 'up'
  | 'down'
  | 'left'
  | 'right'
  | 'pageup'
  | 'pagedown'
  | 'c-c'
  | 'c-b'
  | 'c-s'
  | 'c-x'
  | 'c-o'
  | 'c-r'
  | 'c-l'
  | 'c-u'
  | 'bspace';

function headers(): HeadersInit {
  // The same marker every other mutation carries: a cross-site request cannot
  // add a custom header without a preflight the gate does not grant.
  return { 'X-GC-Request': '1' };
}

export async function readPane(session: string, lines: number, signal?: AbortSignal): Promise<Pane> {
  const q = new URLSearchParams({ session, lines: String(lines) });
  const res = await fetch(`${PANE}?${q.toString()}`, {
    headers: headers(),
    ...(signal ? { signal } : {}),
  });
  if (!res.ok) throw new Error(res.status === 404 ? 'no live pane for this agent' : `pane read failed (${res.status})`);
  return (await res.json()) as Pane;
}

export async function sendPaneKey(session: string, key: PaneKey): Promise<void> {
  const res = await fetch(KEYS, {
    method: 'POST',
    headers: { ...headers(), 'Content-Type': 'application/json' },
    body: JSON.stringify({ session, key }),
  });
  if (!res.ok) throw new Error(res.status === 404 ? 'no live pane for this agent' : `key not sent (${res.status})`);
}

// GET /keys answers {"ok": true} where the helper is. It sends no key, so it is
// safe to ask before anyone taps one. This is what lets the key bar show on the
// transcript too: that view has no pane read to prove the helper is there.
async function probeKeys(): Promise<boolean> {
  const res = await fetch(KEYS, { cache: 'no-store' });
  if (!res.ok) return false;
  const body = (await res.json()) as { ok?: unknown } | null;
  return body?.ok === true;
}

export function useKeysServed(): boolean | null {
  return useServed(KEYS, probeKeys);
}

// Whether this deployment serves a pane at all. Asked once, cheaply, so the
// toggle can be hidden rather than offered and then failing.
export async function paneAvailable(session: string): Promise<boolean> {
  try {
    await readPane(session, 1);
    return true;
  } catch {
    return false;
  }
}
