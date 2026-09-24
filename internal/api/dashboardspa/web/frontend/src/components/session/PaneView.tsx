import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { ansiToReactNodes } from '../../lib/ansi';
import { readPane, sendPaneKey, type Pane, type PaneKey } from '../../lib/pane';
import { keepFocus } from '../../lib/keepFocus';

// The agent's pane, as it is drawn, in a phone-shaped viewport.
//
// Two things make this different from the transcript beside it. It is the real
// thing, so nothing has been re-rendered or summarised away. And it has no
// structure at all -- no message boundaries, no tool calls, no markdown -- so
// it can only be shown, never folded, searched by message, or copied per turn.
//
// Width is the whole problem. A pane is a fixed number of columns and an agent
// window is commonly 80, the mayor's often much more; a phone is about 390 CSS
// pixels. Nothing can make 214 columns readable at that width, so the view does
// not pretend: it scales the rendered block to fit and leaves the operator a
// 1:1 button and the browser's own pinch-zoom for reading a wide pane.
//
// Scaling is a transform, not a font size. At a fractional font size each glyph
// advance rounds on its own and the TUI's box drawing drifts a column here and
// there; laying the block out at a whole font size and scaling the result keeps
// every column where the agent put it.
const BASE_FONT = 13; // px, the size the block is laid out at before scaling
const FIRST_WINDOW = 240; // lines fetched on open
const GROW_BY = 400; // more lines per scroll to the top
const MAX_WINDOW = 4000; // the sidecar's own ceiling; asking for more gains nothing
const POLL_MS = 1000;
const NEAR_TOP = 120;
const NEAR_BOTTOM = 40;

// The keys a text box cannot express. Everything here is a key the operator
// already has in the browser terminal, so the bar adds reach, not privilege.
const BAR: Array<{ key: PaneKey; label: string; hint: string }> = [
  { key: 'escape', label: 'Esc', hint: 'Interrupt' },
  { key: 'c-o', label: '^O', hint: 'Expand output' },
  { key: 'btab', label: '⇧⇥', hint: 'Cycle permission mode' },
  { key: 'tab', label: '⇥', hint: 'Complete' },
  { key: 'up', label: '↑', hint: 'Previous' },
  { key: 'down', label: '↓', hint: 'Next' },
  { key: 'enter', label: '⏎', hint: 'Enter' },
  { key: 'c-c', label: '^C', hint: 'Cancel' },
];

export function PaneView({
  session,
  onNotice,
}: {
  session: string;
  onNotice: (text: string) => void;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const block = useRef<HTMLPreElement>(null);
  const atLive = useRef(true);

  const [pane, setPane] = useState<Pane | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [lines, setLines] = useState(FIRST_WINDOW);
  const [fit, setFit] = useState(true);
  const [scale, setScale] = useState(1);
  const [natural, setNatural] = useState({ w: 0, h: 0 });

  // Poll the tail. The pane has no event stream, and a second is well inside
  // what a person reads as live while costing one small request.
  useEffect(() => {
    let live = true;
    const ac = new AbortController();
    const tick = async () => {
      try {
        const next = await readPane(session, lines, ac.signal);
        if (!live) return;
        setPane(next);
        setError(null);
      } catch (e) {
        if (!live || ac.signal.aborted) return;
        setError(e instanceof Error ? e.message : 'pane read failed');
      }
    };
    void tick();
    const id = window.setInterval(() => void tick(), POLL_MS);
    return () => {
      live = false;
      ac.abort();
      window.clearInterval(id);
    };
  }, [session, lines]);

  const nodes = useMemo(() => (pane ? ansiToReactNodes(pane.text) : []), [pane]);

  // Measure what the block wants to be, then scale that to the viewport. Read
  // after layout so the first paint is already at the right size.
  useLayoutEffect(() => {
    const el = block.current;
    const box = scroller.current;
    if (!el || !box || !pane) return;
    const w = el.scrollWidth;
    const h = el.scrollHeight;
    if (w === 0 || h === 0) return;
    setNatural({ w, h });
    const room = box.clientWidth;
    setScale(fit && w > room ? room / w : 1);
  }, [pane, fit]);

  // While the operator is at the tail, the tail is where the view stays. An
  // interval rather than a list of triggers: the pane changes for reasons the
  // component never sees, and every one of them should keep the bottom in view.
  useEffect(() => {
    const id = window.setInterval(() => {
      if (!atLive.current) return;
      const el = scroller.current;
      if (!el) return;
      if (el.scrollHeight - el.scrollTop - el.clientHeight > 1) el.scrollTop = el.scrollHeight;
    }, 150);
    return () => window.clearInterval(id);
  }, []);

  const onScroll = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    atLive.current = el.scrollHeight - el.scrollTop - el.clientHeight <= NEAR_BOTTOM;
    // Reaching the top asks for more of the scrollback, up to whatever tmux
    // still holds. `at_oldest` is what stops it, so a short session does not
    // keep refetching a window that cannot grow.
    if (el.scrollTop <= NEAR_TOP && pane && !pane.at_oldest && lines < MAX_WINDOW) {
      setLines((n) => Math.min(MAX_WINDOW, n + GROW_BY));
    }
  }, [pane, lines]);

  const press = useCallback(
    async (key: PaneKey, hint: string) => {
      try {
        await sendPaneKey(session, key);
        onNotice(hint);
      } catch {
        onNotice('key not sent');
      }
    },
    [session, onNotice],
  );

  const chip =
    'pointer-events-auto rounded-full bg-surface/85 px-2 py-0.5 text-label uppercase tracking-wider text-fg backdrop-blur';

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        ref={scroller}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-auto overscroll-contain bg-surface-tint"
        style={{ paddingTop: 'calc(env(safe-area-inset-top) + 4.25rem)' }}
      >
        {error && (
          <p className="px-3 py-8 text-center text-accent" role="alert">
            {error}
          </p>
        )}
        {!pane && !error && <p className="px-3 py-8 text-center text-fg-muted italic">Reading the pane.</p>}
        {pane && (
          <>
            {!pane.at_oldest && (
              <p className="py-2 text-center text-label uppercase tracking-wider text-fg-faint">
                scroll up for earlier output
              </p>
            )}
            {pane.at_oldest && (
              <p className="py-2 text-center text-label uppercase tracking-wider text-fg-faint">
                the oldest line this pane still holds
              </p>
            )}
            {/* The scaled block is taken out of flow by the transform, so the
                wrapper carries the size it occupies after scaling. Without it
                the scroller would size itself to the unscaled block and leave a
                screen of empty space below a shrunken pane. */}
            <div
              style={{
                width: natural.w * scale || undefined,
                height: natural.h * scale || undefined,
              }}
            >
              <pre
                ref={block}
                className="w-max font-mono text-fg"
                style={{
                  fontSize: BASE_FONT,
                  lineHeight: 1.25,
                  margin: 0,
                  padding: '0 8px',
                  whiteSpace: 'pre',
                  transform: `scale(${scale})`,
                  transformOrigin: 'top left',
                }}
              >
                {nodes}
              </pre>
            </div>
          </>
        )}
      </div>

      {/* Two rows above the composer: what the view is doing, and the keys the
          composer cannot send. */}
      <div className="flex items-center gap-2 overflow-x-auto px-3 py-1">
        <button
          type="button"
          onMouseDown={keepFocus}
          onClick={() => setFit((f) => !f)}
          className={chip}
          aria-pressed={!fit}
        >
          {fit ? 'Fit' : '1:1'}
        </button>
        {pane && (
          <span className="shrink-0 text-label uppercase tracking-wider text-fg-faint">
            {pane.width}×{pane.height} · {pane.history} back
          </span>
        )}
        {fit && scale < 0.5 && (
          <span className="shrink-0 text-label uppercase tracking-wider text-fg-faint">
            pinch to read
          </span>
        )}
      </div>
      <div className="flex gap-1 overflow-x-auto px-3 pb-1">
        {BAR.map((b) => (
          <button
            key={b.key}
            type="button"
            onMouseDown={keepFocus}
            onClick={() => void press(b.key, b.hint)}
            aria-label={b.hint}
            className="shrink-0 rounded-md bg-surface px-2.5 py-1.5 font-mono text-body text-fg active:bg-surface-tint"
          >
            {b.label}
          </button>
        ))}
      </div>
    </div>
  );
}
