import { cleanup, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import { forgetServedAnswers } from './served';
import { useKeysServed } from './pane';

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

describe('useKeysServed', () => {
  it('is true where the pane helper answers GET /keys', async () => {
    fetchMock.mockResolvedValue(
      new Response('{"ok": true}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    const { result } = renderHook(() => useKeysServed());
    await waitFor(() => expect(result.current).toBe(true));
    // Asking must not press anything: the probe is a GET, never a POST.
    const init = fetchMock.mock.calls[0]![1] as RequestInit | undefined;
    expect(fetchMock.mock.calls[0]![0]).toBe('/keys');
    expect(init?.method ?? 'GET').toBe('GET');
  });

  it('is false on the plain supervisor, where /keys is a 404', async () => {
    fetchMock.mockResolvedValue(new Response('404 page not found\n', { status: 404 }));
    const { result } = renderHook(() => useKeysServed());
    await waitFor(() => expect(result.current).toBe(false));
  });

  it('is false when something answers 200 but is not the helper', async () => {
    fetchMock.mockResolvedValue(new Response('<!doctype html>', { status: 200 }));
    const { result } = renderHook(() => useKeysServed());
    await waitFor(() => expect(result.current).toBe(false));
  });
});
