import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { ansiLines, type Line } from '../../lib/ansi';
import { linkify } from '../../lib/linkify';
import { hideInputBox } from '../../lib/inputbox';
import { reflow } from '../../lib/reflow';
import { readPane, type Pane } from '../../lib/pane';
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
// Focus the message box below the pane. It carries a data attribute rather
// than a ref because the composer is a sibling, not a child.
function focusComposer() {
  document.querySelector<HTMLTextAreaElement>('[data-composer-input]')?.focus();
}

// One element per line, so the view can find the line under the reader's eye
// again after the text changes and put it back where it was.
function LineView({ line }: { line: Line }) {
  const empty = line.every((r) => r.text === '' && !r.hint);
  return (
    <div data-line="">
      {empty ? ' ' : null}
      {line.map((r, i) =>
        r.hint === 'reply' ? (
          <button
            key={i}
            type="button"
            onClick={focusComposer}
            className="my-1 rounded-md border border-dashed border-current px-2 py-1 font-sans text-label uppercase tracking-wider opacity-60 active:opacity-100"
          >
            ↓ reply in the box below
          </button>
        ) : r.link ? (
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
    </div>
  );
}

// This view only reads the pane. The keys that drive it are KeyBar, which the
// session page draws below this view, and below the transcript too.
export function PaneView({ session }: { session: string }) {
  const scroller = useRef<HTMLDivElement>(null);
  const block = useRef<HTMLPreElement>(null);
  const probe = useRef<HTMLSpanElement>(null);
  const atLive = useRef(true);

  // What is shown and what was most recently fetched are different things
  // while the reader is scrolled up. New output would shift the block under
  // their finger -- the window is anchored to the bottom, so every line added
  // there drops one off the top -- so the view holds what they are reading
  // and keeps the newest fetch aside until they come back to the tail.
  const [pane, setPane] = useState<Pane | null>(null);
  const latest = useRef<Pane | null>(null);
  const [live, setLive] = useState(true);
  const [behind, setBehind] = useState(false);
  const growing = useRef(false);
  // The line at the top of the viewport before a change, so it can be put back.
  const anchor = useRef<{ text: string; index: number; top: number } | null>(null);
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
  // The first line whose bottom edge is inside the viewport, with where it sits.
  const topLine = useCallback(() => {
    const box = scroller.current;
    if (!box) return null;
    const boxTop = box.getBoundingClientRect().top;
    const els = Array.from(box.querySelectorAll<HTMLElement>('[data-line]'));
    for (let i = 0; i < els.length; i++) {
      const r = els[i]!.getBoundingClientRect();
      if (r.bottom > boxTop) return { text: els[i]!.textContent ?? '', index: i, top: r.top - boxTop };
    }
    return null;
  }, []);

  useEffect(() => {
    let alive = true;
    const ac = new AbortController();
    const tick = async () => {
      try {
        const next = await readPane(session, lines, ac.signal);
        if (!alive) return;
        latest.current = next;
        setError(null);
        if (atLive.current) {
          setPane(next);
          setBehind(false);
        } else if (growing.current) {
          // The reader asked for more history. Remember the line at the top of
          // the viewport so the layout effect can put it back after the
          // older lines land above it.
          growing.current = false;
          anchor.current = topLine();
          setPane(next);
        } else {
          setBehind(true);
        }
      } catch (e) {
        if (!alive || ac.signal.aborted) return;
        setError(e instanceof Error ? e.message : 'pane read failed');
      }
    };
    void tick();
    const id = window.setInterval(() => void tick(), POLL_MS);
    return () => {
      alive = false;
      ac.abort();
      window.clearInterval(id);
    };
  }, [session, lines, topLine]);

  // After older lines land above, find the remembered line again -- by text,
  // searching outward from where it was -- and scroll so it sits where it did.
  useLayoutEffect(() => {
    const a = anchor.current;
    const box = scroller.current;
    if (!a || !box) return;
    anchor.current = null;
    const els = Array.from(box.querySelectorAll<HTMLElement>('[data-line]'));
    for (let d = 0; d < els.length; d++) {
      for (const i of [a.index + d, a.index - d]) {
        const el = els[i];
        if (el && (el.textContent ?? '') === a.text) {
          const now = el.getBoundingClientRect().top - box.getBoundingClientRect().top;
          box.scrollTop += now - a.top;
          return;
        }
      }
    }
  }, [pane]);

  const jumpToBottom = useCallback(() => {
    atLive.current = true;
    setLive(true);
    setBehind(false);
    if (latest.current) setPane(latest.current);
    const box = scroller.current;
    if (box) box.scrollTop = box.scrollHeight;
  }, []);

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
  // Links are found on the line as the agent wrote it, and the program's own
  // input box -- which the phone cannot type into -- is swapped for a hint
  // that focuses the one it can.
  const linked = useMemo<Line[]>(
    () => (pane ? hideInputBox(ansiLines(pane.text).map((l) => linkify(l, session))).lines : []),
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
    const near = el.scrollHeight - el.scrollTop - el.clientHeight <= NEAR_BOTTOM;
    if (near !== atLive.current) {
      atLive.current = near;
      setLive(near);
      // Scrolled back to the tail by hand: show what arrived meanwhile.
      if (near && latest.current) {
        setPane(latest.current);
        setBehind(false);
      }
    }
    // Reaching the top asks for more of the scrollback, up to whatever tmux
    // still holds. `at_oldest` is what stops it.
    if (el.scrollTop <= NEAR_TOP && pane && !pane.at_oldest && lines < MAX_WINDOW && !growing.current) {
      growing.current = true;
      setLines((n) => Math.min(MAX_WINDOW, n + GROW_BY));
    }
  }, [pane, lines]);

  const chip = (on: boolean) =>
    `pointer-events-auto rounded-full px-2.5 py-0.5 text-label uppercase tracking-wider backdrop-blur ${
      on ? 'bg-fg text-surface' : 'bg-surface/85 text-fg'
    }`;
  const isGrid = mode !== 'wrap';

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="relative flex min-h-0 flex-1 flex-col">
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

      {/* The way back to the tail while the reader is scrolled up. The dot
          says output arrived meanwhile and is being held. */}
      {!live && (
        <button
          type="button"
          onMouseDown={keepFocus}
          onClick={jumpToBottom}
          aria-label="Jump to bottom"
          className="absolute bottom-3 right-3 inline-flex h-11 w-11 items-center justify-center rounded-full bg-fg text-surface shadow-lg active:opacity-80"
        >
          <span aria-hidden="true" className="text-xl leading-none">
            ↓
          </span>
          {behind && (
            <span aria-hidden="true" className="absolute -right-0.5 -top-0.5 h-3 w-3 rounded-full bg-accent ring-2 ring-surface" />
          )}
        </button>
      )}
      </div>

      {/* How the pane is laid out. The key bar sits below this, drawn by the
          session page. */}
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
    </div>
  );
}
