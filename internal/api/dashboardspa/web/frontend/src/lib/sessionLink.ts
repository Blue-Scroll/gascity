// The in-app route for a live session. Kept next to the callers rather than
// inlined so the query contract (where Back goes, what the header reads) has
// one definition.
// `tmux` is the agent's tmux session name, carried so the session view can offer
// the live pane without asking the API which pane belongs to which session id.
// Absent for a session with no pane, and the view simply does not offer one.
export function sessionRoute(
  sessionId: string,
  back?: string,
  label?: string,
  tmux?: string,
): string {
  const q = new URLSearchParams();
  if (back) q.set('back', back);
  if (label) q.set('label', label);
  if (tmux) q.set('tmux', tmux);
  const s = q.toString();
  return `/session/${encodeURIComponent(sessionId)}${s ? `?${s}` : ''}`;
}
