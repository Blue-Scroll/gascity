// The in-app route for a live session. Kept next to the callers rather than
// inlined so the query contract (where Back goes, what the header reads) has
// one definition.
export function sessionRoute(sessionId: string, back?: string, label?: string): string {
  const q = new URLSearchParams();
  if (back) q.set('back', back);
  if (label) q.set('label', label);
  const s = q.toString();
  return `/session/${encodeURIComponent(sessionId)}${s ? `?${s}` : ''}`;
}
