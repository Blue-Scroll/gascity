import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import { forgetServedAnswers } from '../../lib/served';
import { Composer } from './Composer';

let fetchMock: Mock;

beforeEach(() => {
  forgetServedAnswers();
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderComposer(tmuxSession = '') {
  render(
    <Composer
      sessionId="gc-1"
      tmuxSession={tmuxSession}
      running={false}
      model={null}
      onSend={vi.fn().mockResolvedValue(undefined)}
      onInterrupt={vi.fn().mockResolvedValue(undefined)}
      onNotice={vi.fn()}
    />,
  );
}

describe('Composer attach control', () => {
  it('is offered where the upload helper is served', async () => {
    fetchMock.mockResolvedValue(
      new Response('{"ok": true}', { status: 200, headers: { 'Content-Type': 'application/json' } }),
    );
    renderComposer();
    expect(await screen.findByRole('button', { name: 'Attach files' })).toBeTruthy();
  });

  it('is not drawn on the plain supervisor, where /upload is a 404', async () => {
    fetchMock.mockResolvedValue(new Response('404 page not found\n', { status: 404 }));
    renderComposer();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    // Let the answer land, so this checks the answer and not the wait for it.
    await act(async () => {});
    expect(screen.queryByRole('button', { name: 'Attach files' })).toBeNull();
    // The message box and send still work without it.
    expect(screen.getByRole('button', { name: 'Send' })).toBeTruthy();
  });
});

// Answers each sidecar path from `routes`, and everything else the way gc does
// for a sidecar path nobody serves: 404.
function serve(routes: Record<string, () => Response>) {
  fetchMock.mockImplementation((input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://127.0.0.1/');
    const answer = routes[url.pathname];
    return Promise.resolve(
      answer ? answer() : new Response('404 page not found\n', { status: 404 }),
    );
  });
}

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });

function typeInto(value: string) {
  fireEvent.change(screen.getByPlaceholderText('Message…'), { target: { value } });
}

function listed(): string[] {
  const list = screen.queryByRole('list', { name: 'Commands' });
  if (!list) return [];
  return Array.from(list.querySelectorAll('button > span:first-child')).map(
    (el) => el.textContent ?? '',
  );
}

describe('Composer "/" list', () => {
  it("offers this agent's own skills and commands beside the built-ins", async () => {
    serve({
      '/commands': () =>
        json({
          commands: [
            { name: 'spoon-feed', description: 'Surface open decisions', source: 'project' },
            { name: 'core.gc-work', description: 'Work items (beads)', source: 'project' },
            { name: 'ralph-loop:help', description: 'Explain Ralph Loop', source: 'plugin' },
          ],
        }),
    });
    renderComposer('gastown__mayor');
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([u]) => String(u).startsWith('/commands?'))).toBe(true),
    );
    typeInto('/');
    await waitFor(() => expect(listed()).toContain('/spoon-feed'));
    expect(listed()).toContain('/core.gc-work');
    expect(listed()).toContain('/clear');
    expect(screen.getByText(/Surface open decisions/)).toBeTruthy();
  });

  it('asks the sidecar for this pane, with the header the gate lets through', async () => {
    serve({ '/commands': () => json({ commands: [] }) });
    renderComposer('gastown__mayor');
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([u]) => String(u).startsWith('/commands?'))).toBe(true),
    );
    const [url, init] = fetchMock.mock.calls.find(([u]) => String(u).startsWith('/commands?'))!;
    expect(new URL(String(url), 'http://127.0.0.1/').searchParams.get('session')).toBe(
      'gastown__mayor',
    );
    expect((init as RequestInit).headers).toMatchObject({ 'X-GC-Request': '1' });
  });

  it('narrows to what is typed, and a tap puts the command in the box', async () => {
    serve({
      '/commands': () =>
        json({
          commands: [
            { name: 'spoon-feed', description: 'Surface open decisions', source: 'project' },
          ],
        }),
    });
    renderComposer('gastown__mayor');
    typeInto('/spoo');
    await waitFor(() => expect(listed()).toEqual(['/spoon-feed']));
    fireEvent.click(screen.getByText('/spoon-feed'));
    expect((screen.getByPlaceholderText('Message…') as HTMLTextAreaElement).value).toBe(
      '/spoon-feed ',
    );
  });

  it('keeps the built-ins where no sidecar serves /commands', async () => {
    serve({});
    renderComposer('gastown__mayor');
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([u]) => String(u).startsWith('/commands?'))).toBe(true),
    );
    await act(async () => {});
    typeInto('/cl');
    expect(listed()).toEqual(['/clear']);
  });

  it('does not ask at all when the link carried no tmux session', async () => {
    serve({});
    renderComposer('');
    typeInto('/');
    expect(listed()).toContain('/compact');
    await act(async () => {});
    expect(fetchMock.mock.calls.some(([u]) => String(u).startsWith('/commands'))).toBe(false);
  });
});
