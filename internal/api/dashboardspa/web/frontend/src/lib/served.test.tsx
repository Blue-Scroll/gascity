import { cleanup, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import { forgetServedAnswers } from './served';
import { useTerminalServed } from './terminal';
import { useUploadsServed } from './upload';

// The terminal base is read from the document once and kept, so the tag has to
// be there before anything in this file asks for it.
const meta = document.createElement('meta');
meta.name = 'gc-terminal-base';
meta.content = '/term/';
document.head.appendChild(meta);

let fetchMock: Mock;

function answer(status: number, body: string, type: string): Response {
  return new Response(body, { status, headers: { 'Content-Type': type } });
}

// What gc itself answers for a helper path it does not serve.
const GC_404 = () => answer(404, '404 page not found\n', 'text/plain; charset=utf-8');
// What an older gc answered: the app shell, as if the path were a page.
const SPA_SHELL = () => answer(200, '<!doctype html><div id="root"></div>', 'text/html');

beforeEach(() => {
  forgetServedAnswers();
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function settled<T>(hook: () => T): Promise<T> {
  const { result } = renderHook(hook);
  await waitFor(() => expect(result.current).not.toBeNull());
  return result.current;
}

describe('useUploadsServed', () => {
  it('is null until the machine answers', () => {
    fetchMock.mockReturnValue(new Promise(() => {}));
    const { result } = renderHook(() => useUploadsServed());
    expect(result.current).toBeNull();
  });

  it('is true when the upload helper answers ok', async () => {
    fetchMock.mockResolvedValue(answer(200, '{"ok": true, "max_bytes": 1, "ttl_minutes": 120}', 'application/json'));
    expect(await settled(() => useUploadsServed())).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith('/upload', expect.objectContaining({ cache: 'no-store' }));
    // A probe must never write: no method means GET.
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBeUndefined();
  });

  it('is false on the plain supervisor, which answers 404', async () => {
    fetchMock.mockResolvedValue(GC_404());
    expect(await settled(() => useUploadsServed())).toBe(false);
  });

  it('is false when handed the app shell instead of the helper', async () => {
    fetchMock.mockResolvedValue(SPA_SHELL());
    expect(await settled(() => useUploadsServed())).toBe(false);
  });

  it('is false when the request itself fails', async () => {
    fetchMock.mockRejectedValue(new TypeError('network down'));
    expect(await settled(() => useUploadsServed())).toBe(false);
  });

  it('asks the machine once, however many controls want the answer', async () => {
    fetchMock.mockResolvedValue(answer(200, '{"ok": true}', 'application/json'));
    await settled(() => useUploadsServed());
    await settled(() => useUploadsServed());
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe('useTerminalServed', () => {
  it('is true when the terminal page loads', async () => {
    fetchMock.mockResolvedValue(answer(200, '<html>terminal</html>', 'text/html'));
    expect(await settled(() => useTerminalServed())).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith('/term/', expect.objectContaining({ cache: 'no-store' }));
  });

  it('is false on the plain supervisor, which answers 404', async () => {
    fetchMock.mockResolvedValue(GC_404());
    expect(await settled(() => useTerminalServed())).toBe(false);
  });

  it('is false when the proxy has the route but no terminal behind it', async () => {
    fetchMock.mockResolvedValue(answer(503, 'city is not running', 'text/plain'));
    expect(await settled(() => useTerminalServed())).toBe(false);
  });
});
