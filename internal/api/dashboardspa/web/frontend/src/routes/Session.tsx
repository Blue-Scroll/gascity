import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import {
  structuredMessagesFromEnvelope,
  type SessionStructuredMessage,
} from 'gas-city-dashboard-shared';
import { PendingInteractionView } from '../components/structured/StructuredTranscript';
import { MessageCard, resultText, type ToolCall } from '../components/session/MessageCard';
import { ToolSheet } from '../components/session/ToolSheet';
import { useStructuredSessionStream } from '../hooks/useStructuredSessionStream';
import {
  fetchStructuredTranscriptPage,
  interruptSession,
  sendMessageToSession,
} from '../supervisor/sessionReads';
import { useReadOnly } from '../contexts/ReadOnlyContext';
import { Composer } from '../components/session/Composer';
import { PaneView } from '../components/session/PaneView';
import { paneAvailable } from '../lib/pane';
import { keepFocus } from '../lib/keepFocus';

// A session, read the way a conversation is read: the transcript owns the whole
// viewport and every control floats out of its way. The structured plane already
// gives us typed messages and blocks, so this is native DOM — which is what makes
// selection, copy and search behave like the rest of the phone.
//
// History is a window, never the whole log. The newest messages come from the
// live stream; scrolling to the top pulls one page of older ones through the
// `before` cursor, and returning to the live tail drops them again. A session
// with thousands of turns therefore costs the same as a short one.
const LIVE_TAIL = 60; // messages rendered on arrival, newest first
const REVEAL_STEP = 40; // more of the already-fetched segment, per scroll to the top
const OLDER_WINDOW = 400; // messages held above the live tail before the oldest are dropped
const NEAR_TOP = 320; // px from the top that triggers the next page
const NEAR_BOTTOM = 96; // px from the bottom that still counts as "live"

// A tool's output arrives as its own block in a later message, keyed by the id
// of the call that made it. Pairing them here is what lets a call show its
// output without the feed carrying both.
function resultsByCall(messages: SessionStructuredMessage[]): Map<string, string> {
  const map = new Map<string, string>();
  for (const m of messages) {
    for (const b of m.blocks ?? []) {
      if (b.type !== 'tool_result') continue;
      const id = (b as { tool_call_id?: string }).tool_call_id;
      if (id) map.set(id, resultText(b));
    }
  }
  return map;
}

export function SessionPage() {
  const { id = '' } = useParams<{ id: string }>();
  const [params] = useSearchParams();
  const back = params.get('back') ?? '/agents';
  const label = params.get('label') ?? id;
  // The agent's tmux session, carried by the link that got us here. Its presence
  // is what makes the live pane offerable at all.
  const tmux = params.get('tmux') ?? '';
  const readOnly = useReadOnly();
  const state = useStructuredSessionStream(id, true);

  // Two ways to read the same session. The transcript is the city's rendering:
  // markdown, per-message copy, a tool call that opens its command and output,
  // and history that pages back through the whole session. The pane is what the
  // agent is actually drawing, which is the only place a TUI's banner, spinner
  // and prompt exist -- at the cost of every one of those features, because a
  // pane has no message boundaries to hang them on.
  //
  // The choice sticks, because an operator who wants one almost always wants it
  // again. The composer is the same in both: this is the read surface only.
  const [surface, setSurface] = useState<'transcript' | 'pane'>(() => {
    try {
      return localStorage.getItem('gc.session.surface') === 'pane' ? 'pane' : 'transcript';
    } catch {
      return 'transcript';
    }
  });
  // Offered only where a pane is actually served, so the control is never a
  // button that fails. A deployment without the sidecar simply never sees it.
  const [hasPane, setHasPane] = useState(false);
  useEffect(() => {
    if (!tmux) return;
    let live = true;
    void paneAvailable(tmux).then((ok) => {
      if (live) setHasPane(ok);
    });
    return () => {
      live = false;
    };
  }, [tmux]);
  const showPane = surface === 'pane' && hasPane && tmux !== '';
  const toggleSurface = useCallback(() => {
    setSurface((s) => {
      const next = s === 'pane' ? 'transcript' : 'pane';
      try {
        localStorage.setItem('gc.session.surface', next);
      } catch {
        /* a private window is not a reason to refuse the toggle */
      }
      return next;
    });
  }, []);

  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const [older, setOlder] = useState<SessionStructuredMessage[]>([]);
  const [hasOlder, setHasOlder] = useState(true);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [atLive, setAtLive] = useState(true);
  // How much of the fetched segment is on screen. A long session arrives as one
  // segment, and rendering 800 messages costs a phone far more than holding the
  // JSON does — so the DOM is a window that grows as the operator reads back and
  // snaps shut when they return to the tail.
  const [revealed, setRevealed] = useState(LIVE_TAIL);
  const [toast, setToast] = useState('');
  const [tool, setTool] = useState<ToolCall | null>(null);
  // A sent message takes a round trip — the API hands it to the session, the
  // agent writes it to its own log, and only then does the stream carry it back
  // — which on a phone reads as the message vanishing. It is echoed locally the
  // moment it is accepted, and dropped again when the real one arrives.
  const [pending, setPending] = useState<Array<{ key: string; text: string; at: number }>>([]);
  // On a phone the software keyboard covers the bottom of a fixed layout. The
  // visual viewport is the only thing that knows how much room is really left,
  // so the shell is sized from it and the tail stays visible while typing.
  const [viewport, setViewport] = useState<{ height: number; top: number } | null>(null);
  const keepScroll = useRef<number | null>(null);
  const atLiveRef = useRef(true);

  const allLive = state.status === 'ready' ? state.result.items : [];
  const live = allLive.slice(-revealed);
  const moreInSegment = allLive.length > live.length;
  const liveMessages = live.flatMap((i) => (i.kind === 'message' ? [i.message] : []));
  const results = resultsByCall([...older, ...liveMessages]);
  const oldestLiveId = liveMessages[0]?.id;
  const seen = new Set(liveMessages.map((m) => m.id));

  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;
    const sync = () => {
      setViewport({ height: vv.height, top: vv.offsetTop });
      // Keep the newest message in view as the keyboard opens and closes.
      const el = scroller.current;
      if (el && atLiveRef.current) el.scrollTop = el.scrollHeight;
    };
    sync();
    vv.addEventListener('resize', sync);
    vv.addEventListener('scroll', sync);
    return () => {
      vv.removeEventListener('resize', sync);
      vv.removeEventListener('scroll', sync);
    };
  }, []);

  const flash = useCallback((m: string) => {
    setToast(m);
    window.setTimeout(() => setToast(''), 1800);
  }, []);

  const loadOlder = useCallback(async () => {
    // First show more of what is already here; only then ask the server.
    if (moreInSegment) {
      const el = scroller.current;
      keepScroll.current = el ? el.scrollHeight - el.scrollTop : null;
      setRevealed((r) => r + REVEAL_STEP);
      return;
    }
    const cursor = older[0]?.id ?? oldestLiveId;
    if (!cursor || loadingOlder || !hasOlder) return;
    setLoadingOlder(true);
    const el = scroller.current;
    keepScroll.current = el ? el.scrollHeight - el.scrollTop : null;
    try {
      const page = await fetchStructuredTranscriptPage(id, { before: cursor, includeThinking: true });
      const msgs = page.event ? structuredMessagesFromEnvelope(page.event) : [];
      setHasOlder(page.hasOlder && msgs.length > 0);
      if (msgs.length > 0) {
        setOlder((prev) => [...msgs, ...prev].slice(-OLDER_WINDOW));
      }
    } catch {
      flash('could not load older messages');
      setHasOlder(false);
    } finally {
      setLoadingOlder(false);
    }
  }, [older, oldestLiveId, loadingOlder, hasOlder, id, flash, moreInSegment]);

  // Keep the reading position pinned when a page is prepended.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && keepScroll.current !== null) {
      el.scrollTop = el.scrollHeight - keepScroll.current;
      keepScroll.current = null;
    }
  }, [older, revealed]);

  const pinToBottom = useCallback(() => {
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, []);

  // While the operator is at the tail, the tail is where the view stays. Full
  // stop — no list of things that are allowed to move it.
  //
  // Enumerating triggers was wrong twice over. It missed the ones React never
  // sees (a <details> opening, a font or image settling), and it missed new
  // messages outright: the rendered window is capped, so its length sits at the
  // cap and never changes when a message arrives. Watching the scroller itself,
  // on a cheap timer, has no such list to get wrong. It only ever acts when the
  // view has drifted and the operator has not scrolled away.
  useEffect(() => {
    const tick = window.setInterval(() => {
      if (!atLiveRef.current) return;
      const el = scroller.current;
      if (!el) return;
      if (el.scrollHeight - el.scrollTop - el.clientHeight > 1) pinToBottom();
    }, 150);
    return () => window.clearInterval(tick);
  }, [pinToBottom]);

  // Instant for the common case, so the timer is a backstop rather than the
  // thing the operator can feel.
  useEffect(() => {
    const box = content.current;
    if (!box || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => {
      if (atLiveRef.current) pinToBottom();
    });
    ro.observe(box);
    return () => ro.disconnect();
  }, [pinToBottom]);

  // First paint of a session opens at the tail.
  useLayoutEffect(() => {
    if (atLiveRef.current) pinToBottom();
  }, [state.status, pinToBottom]);

  useEffect(() => {
    if (pending.length === 0) return;
    const said = liveMessages
      .filter((m) => m.role === 'user')
      .map((m) =>
        (m.blocks ?? [])
          .filter((b) => b.type === 'text')
          .map((b) => (b as { text?: string }).text ?? '')
          .join('\n'),
      );
    setPending((list) =>
      list.filter((echo) => {
        // The session wraps what it delivers, so a contains-match on the head
        // of the message is what identifies it coming back.
        const head = echo.text.trim().slice(0, 60);
        const landed = head !== '' && said.some((t) => t.includes(head));
        // And never leave an echo up forever if it never returns.
        return !landed && Date.now() - echo.at < 180000;
      }),
    );
  }, [liveMessages, pending.length]);

  const onScroll = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    const nowLive = bottom <= NEAR_BOTTOM;
    atLiveRef.current = nowLive;
    setAtLive(nowLive);
    // Back at the tail: the pages read on the way up are no longer on screen,
    // so drop them rather than grow the document for the rest of the session.
    if (nowLive && (older.length > 0 || revealed !== LIVE_TAIL)) {
      setOlder([]);
      setHasOlder(true);
      setRevealed(LIVE_TAIL);
    }
    if (el.scrollTop < NEAR_TOP) void loadOlder();
  }, [loadOlder, older.length, revealed]);

  const toLive = () => {
    setOlder([]);
    setHasOlder(true);
    setRevealed(LIVE_TAIL);
    setAtLive(true);
    atLiveRef.current = true;
    pinToBottom();
    // The list shrinks back to the live window on the next paint, so pin again
    // once that has happened rather than to the height it has right now.
    requestAnimationFrame(pinToBottom);
  };

  const copy = async (text: string, what: string) => {
    try {
      await navigator.clipboard.writeText(text);
      flash(`${what} copied`);
    } catch {
      flash('clipboard blocked');
    }
  };

  // Echo first, then deliver: if the API refuses, the echo goes and the
  // composer keeps the text, so nothing is silently lost.
  const withEcho = async (text: string, deliver: () => Promise<void>, ok: string) => {
    const key = `echo-${Date.now()}`;
    setPending((list) => [...list, { key, text, at: Date.now() }]);
    toLive();
    try {
      await deliver();
      flash(ok);
    } catch (e) {
      setPending((list) => list.filter((x) => x.key !== key));
      throw e;
    }
  };

  const send = (text: string) => withEcho(text, () => sendMessageToSession(id, text), 'sent');
  const interrupt = (text: string) =>
    withEcho(text, () => interruptSession(id, text), 'interrupted');

  // What the agent is doing, and on what. Both come from the stream, so the
  // composer can offer Stop only while there is something to stop.
  const running = state.status === 'ready' && state.result.activity === 'in-turn';
  const model =
    [...liveMessages]
      .reverse()
      .map((m) => (m as { model?: string }).model)
      .find((v) => typeof v === 'string' && v !== '') ?? null;

  const pill =
    'pointer-events-auto rounded-full border border-rule bg-surface/90 backdrop-blur px-3 min-h-11 inline-flex items-center gap-1 text-label uppercase tracking-wider text-fg-muted shadow-sm focus-mark';

  return (
    <div
      className="fixed inset-x-0 z-50 flex flex-col bg-surface"
      style={
        viewport
          ? { top: viewport.top, height: viewport.height }
          : { top: 0, height: '100dvh' }
      }
    >
      {showPane ? (
        <PaneView session={tmux} onNotice={flash} />
      ) : (
      <div
        ref={scroller}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-3 pb-3"
        style={{ paddingTop: 'calc(env(safe-area-inset-top) + 4.25rem)' }}
      >
        {(hasOlder || moreInSegment) && (
          <div className="py-3 text-center text-label uppercase tracking-wider text-fg-faint">
            {loadingOlder ? 'loading earlier messages' : 'scroll up for earlier messages'}
          </div>
        )}
        {state.status === 'loading' && <p className="py-8 text-center text-fg-muted italic">Fetching transcript.</p>}
        {state.status === 'failed' && (
          <p className="py-8 text-center text-accent" role="alert">
            {state.error}
          </p>
        )}
        <div ref={content} className="space-y-2">
          {older
            .filter((m) => !seen.has(m.id))
            .map((m) => (
              <MessageCard key={`o-${m.id}`} message={m} onCopy={copy} onOpenTool={setTool} />
            ))}
          {live.map((item) =>
            item.kind === 'message' ? (
              <MessageCard
                key={`m-${item.message.id}`}
                message={item.message}
                onCopy={copy}
                onOpenTool={setTool}
              />
            ) : (
              <div key={`p-${item.pending.request_id}`}>
                <PendingInteractionView pending={item.pending} />
              </div>
            ),
          )}
          {pending.map((echo) => (
            <article
              key={echo.key}
              className="rounded-lg bg-surface-tint px-3 py-1.5 opacity-70"
              aria-label="message being delivered"
            >
              <p className="whitespace-pre-wrap break-words text-body text-fg">{echo.text}</p>
              <p className="mt-1 text-label uppercase tracking-wider text-fg-faint">sending…</p>
            </article>
          ))}
        </div>
      </div>
      )}

      {/* Floating controls: a back chip and the session name at the top, the
          composer and a jump-to-live pill at the bottom. Nothing takes a
          permanent slice of a phone screen. */}
      {/* A scrim under the floating chips. They sit over a scroller, so without
          one the line passing behind them reads as part of the chip row --
          worst on the pane, where it is a wall of monospace. */}
      <div
        className="pointer-events-none absolute inset-x-0 top-0 flex items-center gap-2 px-3 py-2"
        style={{
          paddingTop: 'calc(env(safe-area-inset-top) + 0.5rem)',
          background: showPane
            ? 'linear-gradient(to bottom, rgba(29,29,31,0.96) 55%, rgba(29,29,31,0))'
            : undefined,
        }}
      >
        <Link to={back} className={pill}>
          <span aria-hidden="true">←</span> Back
        </Link>
        <span className="pointer-events-none truncate rounded-full bg-surface/80 px-2 text-label uppercase tracking-wider text-fg backdrop-blur">
          {label}
        </span>
        {hasPane && (
          <button
            type="button"
            onMouseDown={keepFocus}
            onClick={toggleSurface}
            className={pill}
            aria-pressed={showPane}
            aria-label={showPane ? 'Show the transcript' : 'Show the live pane'}
          >
            {showPane ? 'Transcript' : 'Live pane'}
          </button>
        )}
        {toast && (
          <span className="pointer-events-none ml-auto rounded-full bg-surface/80 px-2 text-label uppercase tracking-wider text-fg-faint backdrop-blur">
            {toast}
          </span>
        )}
      </div>

      {/* Above the composer, not over it, so it is reachable with the keyboard up. */}
      {!atLive && !showPane && (
        <div className="pointer-events-none absolute inset-x-0 bottom-16 flex justify-center">
          <button type="button" onMouseDown={keepFocus} onClick={toLive} className={pill}>
            Jump to live ↓
          </button>
        </div>
      )}

      {tool && (
        <ToolSheet
          name={tool.name}
          command={tool.command}
          output={results.get(tool.id) ?? null}
          onClose={() => setTool(null)}
          onCopy={copy}
        />
      )}

      {!readOnly && (
        <div style={{ paddingBottom: 'env(safe-area-inset-bottom)' }}>
          <Composer
            sessionId={id}
            running={running}
            model={model}
            onSend={send}
            onInterrupt={interrupt}
            onNotice={flash}
          />
        </div>
      )}
    </div>
  );
}
