import type {
  AgentResponse,
  PendingInteraction,
  RespondSessionResponse,
  SessionRespondInputBody,
} from 'gas-city-dashboard-shared/gc-supervisor';
import { activeCityOrThrow } from '../api/cityBase';
import { agentSessionId } from './agentReads';
import { supervisorApi } from './client';

export interface AgentPendingInteraction {
  agentName: string;
  sessionId: string;
  sessionName: string;
  pending: PendingInteraction;
}

/**
 * Ask each live agent's session whether it waits on the operator.
 * `sessionIdsByName` is agentSessionId's fallback; most rows carry their id.
 */
export async function listAgentPendingInteractions(
  agents: readonly AgentResponse[],
  sessionIdsByName?: ReadonlyMap<string, string>,
): Promise<AgentPendingInteraction[]> {
  const cityName = activeCityOrThrow('list agent pending interactions');
  const candidates = agents.flatMap((agent) => {
    const sessionId = agentSessionId(agent, sessionIdsByName);
    if (agent.session === undefined || sessionId === undefined) return [];
    return [{ agentName: agent.name, sessionId, sessionName: agent.session.name }];
  });

  const pending = await Promise.all(
    candidates.map(async (candidate) => {
      const response = await supervisorApi().sessionPending(cityName, candidate.sessionId);
      if (response.pending === undefined) return null;
      return { ...candidate, pending: response.pending };
    }),
  );
  return pending.filter((item): item is AgentPendingInteraction => item !== null);
}

export async function respondToAgentPendingInteraction(
  sessionId: string,
  body: SessionRespondInputBody,
): Promise<RespondSessionResponse> {
  const cityName = activeCityOrThrow('respond to agent pending interaction');
  return supervisorApi().respondSession(cityName, sessionId, body);
}

export function attachCommand(agentName: string): string {
  return `gc agent attach ${shellToken(agentName)}`;
}

function shellToken(value: string): string {
  if (/^[A-Za-z0-9_./:-]+$/.test(value)) return value;
  return `'${value.replaceAll("'", "'\\''")}'`;
}
