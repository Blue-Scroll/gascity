import { activeCityOrThrow } from '../api/cityBase';
import type {
  AgentResponse,
  ListBodyAgentResponse,
  SessionResponse,
} from 'gas-city-dashboard-shared/gc-supervisor';
import { supervisorApi } from './client';

export type SupervisorAgent = AgentResponse;

export interface SupervisorAgentList extends Omit<ListBodyAgentResponse, 'items'> {
  items: SupervisorAgent[];
}

export async function listSupervisorAgents(): Promise<SupervisorAgentList> {
  const list = await supervisorApi().listAgents(activeCityOrThrow('list supervisor agents'));
  return {
    ...list,
    items: list.items ?? [],
  };
}

/**
 * The id a session route takes (`/session/{id}`) for an agent's live session,
 * or undefined when it has none.
 *
 * gc puts the id on the agent row (`session.id`), so read it from there. Do
 * not wait on the sessions list for it: on a busy town that list took over a
 * minute, and the Agents page never showed its link into the pane
 * (hq-subxy4). `sessionIdsByName` is only the fallback for a row with no id
 * (an older gc, or a session with no GC_SESSION_ID).
 */
export function agentSessionId(
  agent: Pick<SupervisorAgent, 'session'>,
  sessionIdsByName?: ReadonlyMap<string, string>,
): string | undefined {
  const session = agent.session;
  if (session === undefined) return undefined;
  if (session.id) return session.id;
  return sessionIdsByName?.get(session.name);
}

/** Whether any live agent needs the sessions-list fallback of agentSessionId. */
export function agentsLackSessionIds(agents: readonly Pick<SupervisorAgent, 'session'>[]): boolean {
  return agents.some((agent) => agent.session !== undefined && !agent.session.id);
}

/** The fallback map for agentSessionId: tmux session name to session id. */
export function sessionIdsByName(sessions: readonly SessionResponse[]): Map<string, string> {
  const out = new Map<string, string>();
  for (const session of sessions) {
    if (session.session_name) out.set(session.session_name, session.id);
  }
  return out;
}
