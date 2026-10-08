import { useServed } from './served';

// Attachments from the composer.
//
// An agent opens a file by path, so an attached file has to land on the machine
// the agent runs on. gc does not do that. A small helper behind the same proxy
// does, at /upload, and keeps what it writes for a short while only. A machine
// without that helper (the plain supervisor on a laptop) gets a 404 from gc for
// this path, and the composer then offers no attach control at all.
const UPLOAD = '/upload';

// GET /upload answers {"ok": true, ...} when the helper is there. It writes
// nothing, so it is safe to ask before anyone has attached anything.
async function probeUploads(): Promise<boolean> {
  const res = await fetch(UPLOAD, { cache: 'no-store' });
  if (!res.ok) return false;
  const body = (await res.json()) as { ok?: unknown } | null;
  return body?.ok === true;
}

export function useUploadsServed(): boolean | null {
  return useServed(UPLOAD, probeUploads);
}

export type Written = { path: string } | { problem: string };

// Write one attachment and get back the path the agent should read. Called
// only when the message is sent, so a file that is attached and then removed
// never reaches the disk.
export async function writeAttachment(sessionId: string, file: File, name: string): Promise<Written> {
  const body = new FormData();
  body.append('file', file, name);
  body.append('session', sessionId);
  // The marker every dashboard mutation carries. A page on another site cannot
  // add a custom header without a preflight the gate never grants.
  const res = await fetch(UPLOAD, { method: 'POST', body, headers: { 'X-GC-Request': '1' } });
  if (res.status === 404) return { problem: 'attachments are not set up on this machine' };
  if (!res.ok) return { problem: `${name}: attachment failed (${res.status})` };
  const out = (await res.json()) as { path?: string };
  return out.path ? { path: out.path } : { problem: `${name}: attachment failed` };
}
