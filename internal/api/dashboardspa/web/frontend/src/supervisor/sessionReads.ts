import type {
  ListBodySessionResponse,
  OutputTurn,
  SessionResponse,
  SessionTranscriptConversationResponse,
  SessionTranscriptGetResponse,
} from 'gas-city-dashboard-shared/gc-supervisor';
import { isSessionStructuredEvent } from 'gas-city-dashboard-shared';
import type {
  DashboardSession,
  SessionStreamStructuredMessageEvent,
} from 'gas-city-dashboard-shared';
import { activeCityOrThrow } from '../api/cityBase';
import { supervisorApi } from './client';

export type SupervisorSession = SessionResponse;
export type SupervisorSessionList = ListBodySessionResponse;

export type SessionTranscriptView = Omit<SessionTranscriptConversationResponse, 'turns'> & {
  turns: OutputTurn[];
  total_chars: number;
  captured_at: string;
  truncated: boolean;
};

export async function listSupervisorSessions(): Promise<SupervisorSessionList> {
  return supervisorApi().listSessions(activeCityOrThrow('list supervisor sessions'));
}

/**
 * Whether a session row carries its id. While the bead store is slow, GET
 * /sessions answers at once with the live sessions and marks the list partial
 * (vn-fzant5y). Those rows have no id yet, because the id lives on the session
 * bead. Use them to show who is running. Never use one to open, peek at or
 * link a session: check this first.
 */
export function hasSessionId(session: SupervisorSession): boolean {
  return session.id !== '';
}

export async function fetchSupervisorSessionTranscript(
  sessionId: string,
): Promise<SessionTranscriptView> {
  const transcript = await supervisorApi().sessionTranscript(
    activeCityOrThrow('fetch supervisor session transcript'),
    sessionId,
    'conversation',
  );
  return sessionTranscriptView(transcript);
}

/**
 * Fetch a session transcript as `format=structured` and narrow the generated
 * response union at the edge. Returns null when the server fell back to a
 * non-structured response (the caller then renders the conversation transcript
 * instead).
 */
export async function fetchStructuredTranscript(
  sessionId: string,
): Promise<SessionStreamStructuredMessageEvent | null> {
  const transcript = await supervisorApi().sessionTranscriptPage(
    activeCityOrThrow('fetch structured session transcript'),
    sessionId,
    // Thinking text is redacted unless asked for; a reader that offers to show
    // it has to ask, or every fold opens on nothing.
    { format: 'structured', include_thinking: true },
  );
  return structuredTranscriptOrNull(transcript);
}

/**
 * One window of a session's structured transcript. `before` is a message id;
 * the server returns the entries immediately before it, which is how the
 * session view pages backwards without ever holding the whole log.
 */
export async function fetchStructuredTranscriptPage(
  sessionId: string,
  opts: { before?: string; tail?: string; includeThinking?: boolean } = {},
): Promise<{
  event: SessionStreamStructuredMessageEvent | null;
  hasOlder: boolean;
  totalCount: number;
}> {
  const transcript = await supervisorApi().sessionTranscriptPage(
    activeCityOrThrow('fetch structured session transcript page'),
    sessionId,
    {
      format: 'structured',
      tail: opts.tail ?? '0',
      ...(opts.before ? { before: opts.before } : {}),
      ...(opts.includeThinking ? { include_thinking: true } : {}),
    },
  );
  const pagination = (
    transcript as { pagination?: { has_older_messages?: boolean; total_message_count?: number } }
  ).pagination;
  return {
    event: structuredTranscriptOrNull(transcript),
    hasOlder: pagination?.has_older_messages ?? false,
    totalCount: pagination?.total_message_count ?? 0,
  };
}

export async function sendMessageToSession(sessionId: string, message: string): Promise<void> {
  await supervisorApi().sendSessionMessage(
    activeCityOrThrow('send a message to a session'),
    sessionId,
    message,
  );
}

/**
 * Cut the current run short. gc's only interrupt is a submit that replaces the
 * run's remaining work with a message, so an empty one is refused by the API;
 * the caller passes whatever the operator had typed, or a plain stop.
 */
export async function interruptSession(sessionId: string, message: string): Promise<void> {
  await supervisorApi().submitSession(
    activeCityOrThrow('interrupt a session'),
    sessionId,
    message.trim() === '' ? 'stop' : message,
    'interrupt_now',
  );
}

export function structuredTranscriptOrNull(
  transcript: SessionTranscriptGetResponse,
): SessionStreamStructuredMessageEvent | null {
  if (transcript.format !== 'structured') return null;
  if (!isSessionStructuredEvent(transcript)) {
    throw new Error('Malformed structured transcript response.');
  }
  return transcript;
}

export function normalizeSessions(list: ListBodySessionResponse): DashboardSession[] {
  // A row with no id cannot be joined to anything; the list is already partial.
  return (list.items ?? []).filter(hasSessionId).map(normalizeSession);
}

function normalizeSession(session: SessionResponse): DashboardSession {
  const normalized: DashboardSession = {
    id: session.id,
    template: session.template,
    session_name: session.session_name,
    title: session.title,
    state: session.state,
    created_at: session.created_at,
    attached: session.attached,
    running: session.running,
    provider: session.provider,
  };
  if (session.alias !== undefined) normalized.alias = session.alias;
  if (session.reason !== undefined) normalized.reason = session.reason;
  if (session.display_name !== undefined) normalized.display_name = session.display_name;
  if (session.last_active !== undefined) normalized.last_active = session.last_active;
  if (session.rig !== undefined) normalized.rig = session.rig;
  if (session.pool !== undefined) normalized.pool = session.pool;
  if (session.agent_kind !== undefined) normalized.agent_kind = session.agent_kind;
  if (session.model !== undefined) normalized.model = session.model;
  if (session.context_pct !== undefined) normalized.context_pct = session.context_pct;
  if (session.context_window !== undefined) normalized.context_window = session.context_window;
  if (session.activity !== undefined) normalized.activity = session.activity;
  return normalized;
}

export function sessionTranscriptView(
  transcript: SessionTranscriptGetResponse,
  capturedAt: string = new Date().toISOString(),
): SessionTranscriptView {
  if (transcript.format !== 'conversation' && transcript.format !== 'text') {
    throw new Error(`expected conversation transcript, got ${transcript.format}`);
  }
  const turns = transcript.turns ?? [];
  return {
    ...transcript,
    turns,
    total_chars: turns.reduce((sum, turn) => sum + turn.text.length, 0),
    captured_at: capturedAt,
    truncated: false,
  };
}
