import { describe, expect, it } from 'vitest';
import type { SessionResponse } from 'gas-city-dashboard-shared/gc-supervisor';
import { agentSessionId, agentsLackSessionIds, sessionIdsByName } from './agentReads';

const live = (id?: string) => ({
  session: { attached: false, name: 'rig--worker', ...(id === undefined ? {} : { id }) },
});

describe('agentSessionId (hq-subxy4)', () => {
  it('reads the id off the agent row, ahead of the sessions list', () => {
    const fallback = new Map([['rig--worker', 'from-list']]);
    expect(agentSessionId(live('from-row'), fallback)).toBe('from-row');
  });

  it('falls back to the sessions list for a row with no id', () => {
    const fallback = new Map([['rig--worker', 'from-list']]);
    expect(agentSessionId(live(), fallback)).toBe('from-list');
    expect(agentSessionId(live())).toBeUndefined();
  });

  it('has no id for an agent with no live session', () => {
    expect(agentSessionId({}, new Map([['rig--worker', 'from-list']]))).toBeUndefined();
  });

  it('needs the fallback only when a live row has no id', () => {
    expect(agentsLackSessionIds([live('a'), {}])).toBe(false);
    expect(agentsLackSessionIds([live('a'), live()])).toBe(true);
  });

  it('maps a sessions list by tmux name, skipping a session with none', () => {
    const sessions = [
      { id: 'gc-1', session_name: 'rig--worker' },
      { id: 'gc-2', session_name: '' },
    ] as SessionResponse[];
    expect([...sessionIdsByName(sessions)]).toEqual([['rig--worker', 'gc-1']]);
  });

  it('skips a live row that has no id yet, so a name never maps to ""', () => {
    // While the bead store is slow, /sessions answers with live rows that have
    // no id (vn-fzant5y). An empty id would open a session that does not exist.
    const sessions = [
      { id: '', session_name: 'rig--worker' },
      { id: 'gc-2', session_name: 'rig--other' },
    ] as SessionResponse[];
    expect([...sessionIdsByName(sessions)]).toEqual([['rig--other', 'gc-2']]);
  });
});
