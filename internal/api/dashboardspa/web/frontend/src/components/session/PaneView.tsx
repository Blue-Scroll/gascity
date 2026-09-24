import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { ansiLines, type Line } from '../../lib/ansi';
import { linkify } from '../../lib/linkify';
import { reflow } from '../../lib/reflow';
import { readPane, sendPaneKey, type Pane, type PaneKey } from '../../lib/pane';
import { keepFocus } from '../../lib/keepFocus';

// The agent's pane, as it is drawn, in a phone-shaped viewport.
//
// A pane has one width and the phone is not it. The agent laid its output out
// for the laptop's terminal, and nothing here changes that: the phone reads the
// grid over HTTP and is not a tmux client, so it has no say in the width and
// no effect on the terminal. What it can do is lay the same output out again
// for its own screen.
//
// Three ways to look at it, one tap apart:
//
//   wrap   re-wrap every line at the phone's width, at full size, keeping the
//          hanging indents and the colour. The default, because measured on a
//          live Claude Code pane the output is line-shaped -- bullets and
//          continuations, no boxes -- and line-shaped output survives this.
//   fit    the exact grid, scaled to fit the width. Every column where the
//          agent put it, at whatever size that works out to.
//   1:1    the exact grid at full size, panned with a finger.
//
// Scaling in `fit` is a transform, not a font size: at a fractional font size
// each glyph advance rounds on its own and box drawing drifts a column at a
// time, while a whole-number layout scaled afterwards keeps every column.
const BASE_FONT = 13; // px, the size the block is laid out at
const FIRST_WINDOW = 240; // lines fetched on open
const GROW_BY = 400; // more lines per scroll to the top
const MAX_WINDOW = 4000; // the sidecar's own ceiling; asking for more gains nothing
const POLL_MS = 1000;
const NEAR_TOP = 120;
const NEAR_BOTTOM = 40;
const GUTTER = 8; // px of padding either side of the block
// Measured rather than assumed: the monospace advance depends on which font in
// the stack the device actually has, and every width decision here is off by
// whatever a guess is off by.
const PROBE = 'MMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMM';

// The pane is a terminal, so it gets a terminal's surface rather than the
// dashboard's. An agent picks its colours for the background it believes it is
// drawing on -- Claude Code shades panels with a dark 256-colour background and
// writes near-white text into them -- so the same sequences on a light page
// come out as dark blocks holding low-contrast text.
const TERM_BG = '#1d1d1f';
const TERM_FG = '#e8e6e3';

type Mode = 'wrap' | 'fit' | 'one';
const MODE_KEY = 'gc.pane.mode';

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

function loadMode(): Mode {
  try {
    const m = localStorage.getItem(MODE_KEY);
    return m === 'fit' || m === 'one' ? m : 'wrap';
  } catch {
    return 'wrap';
  }
}

// A run with a link is an anchor. A URL opens where it points; a file path
// opens through the sidecar, which decides whether the phone views it or saves
// it. Both open in a new tab, so a home-screen app with no URL bar still has a
// way back. Long-press gives the phone's own copy menu, which is the copy the
// pane cannot otherwise offer.
function LineView({ line }: { line: Line }) {
  return (
    <>
      {line.map((r, i) =>
        r.link ? (
          <a
            key={i}
            href={r.link.href}
            target="_blank"
            rel="noopener noreferrer"
            className={r.className}
            style={{ ...r.style, textDecoration: 'underline', textDecorationStyle: 'dotted', textUnderlineOffset: 3 }}
          >
            {r.text}
          </a>
        ) : (
          <span key={i} className={r.className} style={r.style}>
            {r.text}
          </span>
        ),
      )}
      {'\n'}
    </>
  );
}

export function PaneView({
  session,
  onNotice,
}: {
  session: string;
  onNotice: (text: string) => void;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const block = useRef<HTMLPreElement>(null);
  const probe = useRef<HTMLSpanElement>(null);
  const atLive = useRef(true);

  const [pane, setPane] = useState<Pane | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [lines, setLines] = useState(FIRST_WINDOW);
  const [mode, setModeState] = useState<Mode>(loadMode);
  const [cols, setCols] = useState(0);
  const [scale, setScale] = useState(1);
  const [natural, setNatural] = useState({ w: 0, h: 0 });

  const setMode = useCallback((m: Mode) => {
    setModeState(m);
    try {
      localStorage.setItem(MODE_KEY, m);
    } catch {
      /* a private window is not a reason to refuse the switch */
    }
  }, []);

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

  // How many columns this screen holds at full size. Re-measured when the
  // viewport changes, which on a phone means rotating it.
  const measureCols = useCallback(() => {
    const box = scroller.current;
    const pr = probe.current;
    if (!box || !pr) return;
    const advance = pr.offsetWidth / PROBE.length;
    if (advance <= 0) return;
    setCols(Math.max(20, Math.floor((box.clientWidth - GUTTER * 2) / advance)));
  }, []);
  useLayoutEffect(() => {
    measureCols();
    const box = scroller.current;
    if (!box || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => measureCols());
    ro.observe(box);
    return () => ro.disconnect();
  }, [measureCols]);

  // Links are found on the line as the agent wrote it, before any wrapping, so
  // a URL broken across two visual lines is tappable on both.
  const linked = useMemo<Line[]>(
    () => (pane ? ansiLines(pane.text).map((l) => linkify(l, session)) : []),
    [pane, session],
  );
  const wrapped = useMemo<Line[]>(
    () => (mode === 'wrap' && cols > 0 ? reflow(linked, cols) : []),
    [linked, mode, cols],
  );

  // Grid modes: measure what the block wants to be, then scale that to fit the
  // pane's CURRENT width. Not the widest line in the buffer: a pane that used to
  // be wider leaves long lines behind, and fitting those would shrink what the
  // agent is writing now to suit history nobody is reading.
  useLayoutEffect(() => {
    if (mode === 'wrap') {
      setScale(1);
      return;
    }
    const el = block.current;
    const box = scroller.current;
    const pr = probe.current;
    if (!el || !box || !pane || !pr) return;
    const w = el.scrollWidth;
    const h = el.scrollHeight;
    if (w === 0 || h === 0) return;
    setNatural({ w, h });
    // offsetWidth, not a client rect: the probe would otherwise report a width
    // already multiplied by the previous scale, a feedback loop that settles on
    // a number that merely looks plausible.
    const advance = pr.offsetWidth / PROBE.length;
    const want = pane.width * advance + GUTTER * 2;
    const room = box.clientWidth;
    setScale(mode === 'fit' && want > room ? room / want : 1);
  }, [pane, mode]);

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
    // still holds. `at_oldest` is what stops it.
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

  const chip = (on: boolean) =>
    `pointer-events-auto rounded-full px-2.5 py-0.5 text-label uppercase tracking-wider backdrop-blur ${
      on ? 'bg-fg text-surface' : 'bg-surface/85 text-fg'
    }`;
  const isGrid = mode !== 'wrap';

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        ref={scroller}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-auto overscroll-contain"
        style={{
          paddingTop: 'calc(env(safe-area-inset-top) + 4.25rem)',
          background: TERM_BG,
          color: TERM_FG,
        }}
      >
        {/* The probe lives here, outside anything scaled, in the block's font. */}
        <span
          ref={probe}
          aria-hidden="true"
          className="font-mono"
          style={{ position: 'absolute', visibility: 'hidden', whiteSpace: 'pre', fontSize: BASE_FONT }}
        >
          {PROBE}
        </span>
        {error && (
          <p className="px-3 py-8 text-center text-accent" role="alert">
            {error}
          </p>
        )}
        {!pane && !error && <p className="px-3 py-8 text-center italic opacity-60">Reading the pane.</p>}
        {pane && (
          <>
            <p className="py-2 text-center text-label uppercase tracking-wider opacity-50">
              {pane.at_oldest ? 'the oldest line this pane still holds' : 'scroll up for earlier output'}
            </p>
            {mode === 'wrap' ? (
              <pre
                className="font-mono"
                style={{
                  color: TERM_FG,
                  fontSize: BASE_FONT,
                  lineHeight: 1.3,
                  margin: 0,
                  padding: `0 ${GUTTER}px`,
                  whiteSpace: 'pre',
                }}
              >
                {wrapped.map((line, i) => (
                  <LineView key={i} line={line} />
                ))}
              </pre>
            ) : (
              // The scaled block is taken out of flow by the transform, so the
              // wrapper carries the size it occupies after scaling.
              <div style={{ width: natural.w * scale || undefined, height: natural.h * scale || undefined }}>
                <pre
                  ref={block}
                  className="w-max font-mono"
                  style={{
                    color: TERM_FG,
                    fontSize: BASE_FONT,
                    lineHeight: 1.25,
                    margin: 0,
                    padding: `0 ${GUTTER}px`,
                    whiteSpace: 'pre',
                    transform: `scale(${scale})`,
                    transformOrigin: 'top left',
                  }}
                >
                  {linked.map((line, i) => (
                    <LineView key={i} line={line} />
                  ))}
                </pre>
              </div>
            )}
          </>
        )}
      </div>

      {/* Two rows above the composer: how the pane is laid out, and the keys
          the composer cannot send. */}
      <div className="flex items-center gap-2 overflow-x-auto px-3 py-1">
        <button type="button" onMouseDown={keepFocus} onClick={() => setMode('wrap')} className={chip(!isGrid)} aria-pressed={!isGrid}>
          Wrap
        </button>
        <button type="button" onMouseDown={keepFocus} onClick={() => setMode('fit')} className={chip(mode === 'fit')} aria-pressed={mode === 'fit'}>
          Fit
        </button>
        <button type="button" onMouseDown={keepFocus} onClick={() => setMode('one')} className={chip(mode === 'one')} aria-pressed={mode === 'one'}>
          1:1
        </button>
        {pane && (
          <span className="shrink-0 text-label uppercase tracking-wider text-fg-faint">
            {isGrid ? `${pane.width}×${pane.height}` : `${cols} cols`} · {pane.history} back
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
