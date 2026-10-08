import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import { KeyBar } from './KeyBar';

let fetchMock: Mock;

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const ok = () =>
  new Response('{"sent": "x"}', { status: 200, headers: { 'Content-Type': 'application/json' } });

// The key each POST to /keys carried, in order.
function sentKeys(): string[] {
  return fetchMock.mock.calls
    .filter(([url]) => url === '/keys')
    .map(([, init]) => (JSON.parse(String((init as RequestInit).body)) as { key: string }).key);
}

describe('KeyBar', () => {
  it('sends ctrl+c, ctrl+s, ctrl+b and ctrl+x to the pane by their allowlist names', async () => {
    fetchMock.mockImplementation(async () => ok());
    render(<KeyBar session="agent__x" readOnly={false} onNotice={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    fireEvent.click(screen.getByRole('button', { name: 'Stash prompt' }));
    fireEvent.click(screen.getByRole('button', { name: 'Send to background' }));
    fireEvent.click(screen.getByRole('button', { name: 'Stop or delete' }));
    await waitFor(() => expect(sentKeys()).toEqual(['c-c', 'c-s', 'c-b', 'c-x']));
    const body = JSON.parse(String((fetchMock.mock.calls[0]![1] as RequestInit).body)) as {
      session: string;
    };
    expect(body.session).toBe('agent__x');
  });

  it('labels the ctrl keys the way a terminal writes them', () => {
    render(<KeyBar session="agent__x" readOnly={false} onNotice={vi.fn()} />);
    expect(screen.getByRole('button', { name: 'Cancel' }).textContent).toBe('^C');
    expect(screen.getByRole('button', { name: 'Stash prompt' }).textContent).toBe('^S');
    expect(screen.getByRole('button', { name: 'Send to background' }).textContent).toBe('^B');
    expect(screen.getByRole('button', { name: 'Stop or delete' }).textContent).toBe('^X');
  });

  it('says what the key did once the pane took it', async () => {
    fetchMock.mockImplementation(async () => ok());
    const onNotice = vi.fn();
    render(<KeyBar session="agent__x" readOnly={false} onNotice={onNotice} />);
    fireEvent.click(screen.getByRole('button', { name: 'Send to background' }));
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith('Send to background'));
  });

  it('says why when the helper has no pane for this agent', async () => {
    fetchMock.mockImplementation(
      async () => new Response('{"error": "no such agent pane"}', { status: 404 }),
    );
    const onNotice = vi.fn();
    render(<KeyBar session="agent__x" readOnly={false} onNotice={onNotice} />);
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith('no live pane for this agent'));
  });

  it('is drawn but disabled when the dashboard is read-only, and sends nothing', () => {
    render(<KeyBar session="agent__x" readOnly onNotice={vi.fn()} />);
    const cancel = screen.getByRole('button', { name: 'Cancel' }) as HTMLButtonElement;
    expect(cancel.disabled).toBe(true);
    fireEvent.click(cancel);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
