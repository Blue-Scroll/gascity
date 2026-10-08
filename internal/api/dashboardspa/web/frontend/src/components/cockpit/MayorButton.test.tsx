import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { SessionResponse } from 'gas-city-dashboard-shared/gc-supervisor';
import { MAYOR_AGENT, MayorButton, mayorLink } from './MayorButton';

function session(overrides: Partial<SessionResponse>): SessionResponse {
  return {
    id: 'hq-x',
    template: 'worker',
    session_name: 'worker',
    title: 'Worker',
    provider: 'claude',
    state: 'active',
    attached: false,
    running: true,
    created_at: '2026-09-25T10:00:00Z',
    ...overrides,
  };
}

function mayor(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return session({
    id: 'hq-eh0ta',
    template: MAYOR_AGENT,
    alias: MAYOR_AGENT,
    session_name: 'gastown__mayor',
    title: MAYOR_AGENT,
    ...overrides,
  });
}

function list(items: SessionResponse[], partial?: boolean) {
  return { items, total: items.length, ...(partial === undefined ? {} : { partial }) };
}

function renderButton(items: SessionResponse[]) {
  return render(
    <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
      <MayorButton link={mayorLink(list(items), false)} />
    </MemoryRouter>,
  );
}

describe('mayorLink', () => {
  it('finds the mayor by the agent name gastown.mayor', () => {
    expect(MAYOR_AGENT).toBe('gastown.mayor');
    const link = mayorLink(
      list([session({ id: 'hq-deacon', template: 'gastown.deacon' }), mayor()]),
      false,
    );
    expect(link).toEqual({
      kind: 'running',
      to: '/session/hq-eh0ta?back=%2F&label=Mayor&tmux=gastown__mayor',
    });
  });

  it('follows the new id when the mayor restarts, because nothing stores the old one', () => {
    const before = mayorLink(list([mayor({ id: 'hq-first' })]), false);
    const after = mayorLink(list([mayor({ id: 'hq-second' })]), false);
    expect(before).toMatchObject({
      kind: 'running',
      to: expect.stringContaining('/session/hq-first?'),
    });
    expect(after).toMatchObject({
      kind: 'running',
      to: expect.stringContaining('/session/hq-second?'),
    });
  });

  it('ignores a session that only carries the mayor name in its title or alias', () => {
    const impostor = session({ id: 'hq-other', title: MAYOR_AGENT, alias: MAYOR_AGENT });
    expect(mayorLink(list([impostor]), false)).toEqual({ kind: 'not-running' });
  });

  it('links the newest running mayor and skips one that stopped', () => {
    const link = mayorLink(
      list([
        mayor({ id: 'hq-old', created_at: '2026-09-24T10:00:00Z' }),
        mayor({ id: 'hq-new', created_at: '2026-09-25T10:00:00Z' }),
        mayor({ id: 'hq-stopped', running: false, created_at: '2026-09-25T11:00:00Z' }),
      ]),
      false,
    );
    expect(link).toMatchObject({ to: expect.stringContaining('/session/hq-new?') });
  });

  it('says not running when the list has no running mayor', () => {
    expect(mayorLink(list([session({})]), false)).toEqual({ kind: 'not-running' });
    expect(mayorLink(list([mayor({ running: false })]), false)).toEqual({ kind: 'not-running' });
    expect(mayorLink({ items: null }, false)).toEqual({ kind: 'not-running' });
  });

  it('keeps checking while the list is on its way', () => {
    expect(mayorLink(undefined, true)).toEqual({ kind: 'checking' });
  });

  it('keeps checking when a partial list leaves the mayor out, or has no id for it yet', () => {
    expect(mayorLink(list([session({})], true), false)).toEqual({ kind: 'checking' });
    expect(mayorLink(list([mayor({ id: '' })]), false)).toEqual({ kind: 'checking' });
    // The row GET /sessions sends while the bead store is slow (vn-fzant5y):
    // built from the live session, so no id and no created_at.
    expect(mayorLink(list([mayor({ id: '', created_at: '' })], true), false)).toEqual({
      kind: 'checking',
    });
  });

  it('trusts a partial list that does carry a stopped mayor', () => {
    expect(mayorLink(list([mayor({ running: false })], true), false)).toEqual({
      kind: 'not-running',
    });
  });

  it('never calls a failed read "not running"', () => {
    expect(mayorLink(undefined, false)).toEqual({ kind: 'unknown' });
  });
});

describe('<MayorButton>', () => {
  afterEach(cleanup);

  it('is a link into the live mayor session when the mayor is running', () => {
    renderButton([mayor()]);
    const link = screen.getByRole('link', { name: 'Mayor' });
    expect(link.getAttribute('href')).toBe(
      '/session/hq-eh0ta?back=%2F&label=Mayor&tmux=gastown__mayor',
    );
    expect(link.className).toContain('min-h-11');
  });

  it('is a disabled button saying "Mayor not running" when no mayor runs', () => {
    renderButton([session({})]);
    const button = screen.getByRole('button', { name: 'Mayor not running' });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('shows at once as a busy, disabled button while checking', () => {
    render(<MayorButton link={{ kind: 'checking' }} />);
    const button = screen.getByRole('button', { name: 'Mayor' });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    expect(button.getAttribute('aria-busy')).toBe('true');
  });

  it('stays a 44px tap target in every state', () => {
    render(<MayorButton link={{ kind: 'unknown' }} />);
    expect(screen.getByRole('button', { name: 'Mayor' }).className).toContain('min-h-11');
  });
});
