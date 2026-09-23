// A browser terminal into an agent's tmux pane, when the deployment provides one.
//
// The dashboard does not know how a terminal is served — that is the operator's
// choice (a local ttyd behind the same reverse proxy, or anything else that
// takes a session name). It only reads a base URL from the document:
//
//   <meta name="gc-terminal-base" content="/term/" />
//
// With no meta tag the feature is simply absent: no links, no route, no change
// to any existing view. The base is required to be same-origin and absolute so
// a stray value cannot point the operator's session at another host.
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

export function terminalEnabled(): boolean {
  return terminalBase() !== null;
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
