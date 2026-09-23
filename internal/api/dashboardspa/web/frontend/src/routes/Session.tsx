import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import {
  structuredMessagesFromEnvelope,
  type SessionStructuredMessage,
} from 'gas-city-dashboard-shared';
import { PendingInteractionView } from '../components/structured/StructuredTranscript';
import { MessageCard } from '../components/session/MessageCard';
import { useStructuredSessionStream } from '../hooks/useStructuredSessionStream';
import {
  fetchStructuredTranscriptPage,
  interruptSession,
  sendMessageToSession,
} from '../supervisor/sessionReads';
import { useReadOnly } from '../contexts/ReadOnlyContext';
import { Composer } from '../components/session/Composer';

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
const NEAR_BOTTOM = 48; // px from the bottom that still counts as "live"

function messageText(m: SessionStructuredMessage): string {
  return (m.blocks ?? [])
    .map((b) => {
      if (b.type === 'text') return b.text ?? '';
      if (b.type === 'tool_use') return `[${b.name}]`;
      if (b.type === 'tool_result') return typeof b.content === 'string' ? b.content : '';
      return '';
    })
    .filter(Boolean)
    .join('\n\n')
    .trim();
}

export function SessionPage() {
  const { id = '' } = useParams<{ id: string }>();
  const [params] = useSearchParams();
  const back = params.get('back') ?? '/agents';
  const label = params.get('label') ?? id;
  const readOnly = useReadOnly();
  const state = useStructuredSessionStream(id, true);

  const scroller = useRef<HTMLDivElement>(null);
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
      const page = await fetchStructuredTranscriptPage(id, { before: cursor });
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

  // Follow the tail while the operator is at the bottom.
  useEffect(() => {
    if (!atLive) return;
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [live.length, atLive]);

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
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  };

  const copyMessage = async (m: SessionStructuredMessage) => {
    try {
      await navigator.clipboard.writeText(messageText(m));
      flash('copied');
    } catch {
      flash('clipboard blocked');
    }
  };

  const send = async (text: string) => {
    await sendMessageToSession(id, text);
    toLive();
    flash('sent');
  };

  const interrupt = async (text: string) => {
    await interruptSession(id, text);
    toLive();
    flash('interrupted');
  };

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
        <div className="space-y-2">
          {older
            .filter((m) => !seen.has(m.id))
            .map((m) => (
              <MessageCard key={`o-${m.id}`} message={m} onCopy={copyMessage} />
            ))}
          {live.map((item) =>
            item.kind === 'message' ? (
              <MessageCard key={`m-${item.message.id}`} message={item.message} onCopy={copyMessage} />
            ) : (
              <div key={`p-${item.pending.request_id}`}>
                <PendingInteractionView pending={item.pending} />
              </div>
            ),
          )}
        </div>
      </div>

      {/* Floating controls: a back chip and the session name at the top, the
          composer and a jump-to-live pill at the bottom. Nothing takes a
          permanent slice of a phone screen. */}
      <div
        className="pointer-events-none absolute inset-x-0 top-0 flex items-center gap-2 px-3 py-2"
        style={{ paddingTop: 'calc(env(safe-area-inset-top) + 0.5rem)' }}
      >
        <Link to={back} className={pill}>
          <span aria-hidden="true">←</span> Back
        </Link>
        <span className="pointer-events-none truncate rounded-full bg-surface/80 px-2 text-label uppercase tracking-wider text-fg backdrop-blur">
          {label}
        </span>
        {toast && (
          <span className="pointer-events-none ml-auto rounded-full bg-surface/80 px-2 text-label uppercase tracking-wider text-fg-faint backdrop-blur">
            {toast}
          </span>
        )}
      </div>

      {/* Above the composer, not over it, so it is reachable with the keyboard up. */}
      {!atLive && (
        <div className="pointer-events-none absolute inset-x-0 bottom-16 flex justify-center">
          <button type="button" onClick={toLive} className={pill}>
            Jump to live ↓
          </button>
        </div>
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
