import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
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

function renderComposer() {
  render(
    <Composer
      sessionId="gc-1"
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
