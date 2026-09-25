import { useCallback } from 'react';
import { sendPaneKey, type PaneKey } from '../../lib/pane';
import { keepFocus } from '../../lib/keepFocus';
import { READ_ONLY_CONTROL_TITLE } from '../../contexts/ReadOnlyContext';

// The keys a text box cannot express, sent straight to the agent's tmux pane.
// Everything here is a key the operator already has in the browser terminal, so
// the bar adds reach, not privilege.
//
// The machine's pane helper holds the real allowlist (KEYS in gas-city's
// assets/scripts/dashboard-upload.py). A key that is not on it comes back 400,
// so a new key goes there first, then into PaneKey, then here.
//
// The session page draws one bar, above the composer, whichever way the session
// is being read. The pane and the transcript are two ways to read a session;
// the keys go to the same pane either way.
//
// The ctrl keys come first, and the bar wraps instead of scrolling sideways. A
// key pushed off the right edge of a phone is a key nobody finds. These eleven
// fit one row on a phone 360px wide or more; a new key can push the last one
// onto a second row, so measure at 390px after adding one.
const BAR: ReadonlyArray<{ key: PaneKey; label: string; hint: string }> = [
  { key: 'escape', label: 'Esc', hint: 'Interrupt' },
  { key: 'c-c', label: '^C', hint: 'Cancel' },
  { key: 'c-s', label: '^S', hint: 'Stash prompt' },
  { key: 'c-b', label: '^B', hint: 'Send to background' },
  // Claude Code stops or deletes the picked item on ^X. It also starts its ctrl+x
  // chords, so a key sent after it is read as the chord's second key.
  { key: 'c-x', label: '^X', hint: 'Stop or delete' },
  { key: 'c-o', label: '^O', hint: 'Expand output' },
  { key: 'btab', label: '⇧⇥', hint: 'Cycle permission mode' },
  { key: 'tab', label: '⇥', hint: 'Complete' },
  { key: 'up', label: '↑', hint: 'Previous' },
  { key: 'down', label: '↓', hint: 'Next' },
  { key: 'enter', label: '⏎', hint: 'Enter' },
];

export function KeyBar({
  session,
  readOnly,
  onNotice,
}: {
  // The agent's tmux session name, not the gc session id.
  session: string;
  // A key press drives the agent, so it is a mutation. The keys go to the
  // machine's pane helper, not to gc, so gc's read-only gate never sees them:
  // the bar has to honour read-only itself.
  readOnly: boolean;
  onNotice: (text: string) => void;
}) {
  const press = useCallback(
    async (key: PaneKey, hint: string) => {
      try {
        await sendPaneKey(session, key);
        onNotice(hint);
      } catch (e) {
        onNotice(e instanceof Error ? e.message : 'key not sent');
      }
    },
    [session, onNotice],
  );

  return (
    <div className="flex flex-wrap gap-1 px-2 py-1">
      {BAR.map((b) => (
        <button
          key={b.key}
          type="button"
          onMouseDown={keepFocus}
          onClick={() => void press(b.key, b.hint)}
          disabled={readOnly}
          aria-label={b.hint}
          title={readOnly ? READ_ONLY_CONTROL_TITLE : b.hint}
          className="shrink-0 rounded-md bg-surface px-1.5 py-1.5 font-mono text-body text-fg active:bg-surface-tint disabled:opacity-50"
        >
          {b.label}
        </button>
      ))}
    </div>
  );
}
