import { useServed } from './served';

// A browser terminal into an agent's tmux pane, when the deployment provides one.
//
// The dashboard does not know how a terminal is served. That is the operator's
// choice (a local ttyd behind the same reverse proxy, or anything else that
// takes a session name). It only reads a base URL from the document:
//
//   <meta name="gc-terminal-base" content="/term/" />
//
// With no meta tag the feature is simply absent: no links, no route, no change
// to any existing view. The base is required to be same-origin and absolute so
// a stray value cannot point the operator's session at another host.
//
// The meta tag ships in every build, so it only says where a terminal WOULD be.
// Whether one is really there is asked of the machine (useTerminalServed).
const META = 'gc-terminal-base';

let cached: string | null | undefined;

export function terminalBase(): string | null {
  if (cached !== undefined) return cached;
  cached = null;
  const el = document.querySelector<HTMLMetaElement>(`meta[name="${META}"]`);
  const raw = el?.content?.trim();
  if (raw && raw.startsWith('/') && !raw.startsWith('//')) {
    cached = raw.endsWith('/') ? raw : `${raw}/`;
  }
  return cached;
}

// gc answers 404 for /term/ (sidecarPaths in internal/api/dashboardspa/handler.go),
// and a proxy that has the route but no terminal running behind it answers an
// error. Only the status matters. The body is the terminal's whole page, so the
// request is dropped as soon as the headers arrive. Loading that page does not
// open a terminal: that only happens when a page connects its socket.
async function probeTerminal(): Promise<boolean> {
  const base = terminalBase();
  if (base === null) return false;
  const ac = new AbortController();
  try {
    const res = await fetch(base, { cache: 'no-store', signal: ac.signal });
    return res.ok;
  } finally {
    ac.abort();
  }
}

// `null` while the machine has not answered yet.
export function useTerminalServed(): boolean | null {
  return useServed('terminal', probeTerminal);
}

// The URL the terminal frame loads for one tmux session.
export function terminalFrameUrl(session: string): string | null {
  const base = terminalBase();
  if (base === null || session === '') return null;
  return `${base}?arg=${encodeURIComponent(session)}`;
}

// The in-app route that frames it, so a home-screen web app (which has no URL
// bar and no back gesture) always keeps a way back.
export function terminalRoute(session: string, back?: string): string {
  const q = back ? `?back=${encodeURIComponent(back)}` : '';
  return `/terminal/${encodeURIComponent(session)}${q}`;
}
