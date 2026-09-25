import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { getCached, getCachedFetchedAt, invalidate } from '../api/cache';
import { useCachedData } from './useCachedData';

afterEach(() => {
  cleanup();
  invalidate('');
});

describe('useCachedData', () => {
  it('does not let a stale fetch overwrite state after the cache key changes', async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const fetchers: Record<string, () => Promise<string>> = {
      first: vi.fn(() => first.promise),
      second: vi.fn(() => second.promise),
    };

    const { result, rerender } = renderHook(
      ({ cacheKey }: { cacheKey: string }) =>
        useCachedData(cacheKey, fetcherFor(fetchers, cacheKey)),
      { initialProps: { cacheKey: 'first' } },
    );

    expect(result.current.loading).toBe(true);

    rerender({ cacheKey: 'second' });

    await act(async () => {
      second.resolve('second result');
      await second.promise;
    });
    await waitFor(() => expect(result.current.data).toBe('second result'));

    await act(async () => {
      first.resolve('first result');
      await first.promise;
    });

    expect(result.current.data).toBe('second result');
    expect(result.current.loading).toBe(false);
    expect(result.current.error).toBeNull();
    expect(getCached<string>('first')).toBeUndefined();
    expect(getCached<string>('second')).toBe('second result');
  });

  it('runs a refresh after the fetch in flight lands, and the refresh result wins', async () => {
    const slow = deferred<string>();
    const fast = deferred<string>();
    const cacheKey = 'run-run:active';
    const refreshFetcher = vi.fn(() => fast.promise);

    const { result } = renderHook(() =>
      useCachedData(cacheKey, () => slow.promise, { refreshFetcher }),
    );

    let refreshPromise: Promise<void> | undefined;
    act(() => {
      refreshPromise = result.current.refresh();
    });
    // The refresh waits for the mount fetch instead of racing it.
    expect(refreshFetcher).not.toHaveBeenCalled();

    await act(async () => {
      slow.resolve('first result');
      await slow.promise;
    });
    await waitFor(() => expect(result.current.data).toBe('first result'));
    expect(refreshFetcher).toHaveBeenCalledTimes(1);

    await act(async () => {
      fast.resolve('fresh result');
      await refreshPromise;
    });
    expect(result.current.data).toBe('fresh result');
    expect(getCached<string>(cacheKey)).toBe('fresh result');
    expect(result.current.loading).toBe(false);
  });

  it('never aborts a slow fetch for a refresh storm, and folds the storm into one more fetch (hq-subxy4)', async () => {
    // A page refreshes on every live event. When each refresh aborted the
    // fetch before it, a list slower than the event rate never landed.
    const runs: { signal: AbortSignal; result: ReturnType<typeof deferred<string>> }[] = [];
    const fetcher = vi.fn((signal: AbortSignal) => {
      const result = deferred<string>();
      signal.addEventListener('abort', () => result.reject(new Error('aborted')));
      runs.push({ signal, result });
      return result.promise;
    });

    const { result } = renderHook(() => useCachedData('sessions:storm', fetcher));

    const waits: Promise<void>[] = [];
    act(() => {
      for (let i = 0; i < 5; i += 1) waits.push(result.current.refresh());
      waits.push(result.current.cheapRefresh());
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(runs[0]!.signal.aborted).toBe(false);

    await act(async () => {
      runs[0]!.result.resolve('slow list');
      await runs[0]!.result.promise;
    });
    await waitFor(() => expect(result.current.data).toBe('slow list'));
    expect(fetcher).toHaveBeenCalledTimes(2);

    await act(async () => {
      runs[1]!.result.resolve('after the storm');
      await Promise.all(waits);
    });
    expect(result.current.data).toBe('after the storm');
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('keeps a queued full refresh when a cheap refresh arrives after it', async () => {
    const mount = deferred<string>();
    const full = vi.fn(() => Promise.resolve('full'));
    const cheap = vi.fn(() => Promise.resolve('cheap'));

    const { result } = renderHook(() =>
      useCachedData('runs:queued-full', () => mount.promise, {
        refreshFetcher: full,
        sseRefreshFetcher: cheap,
      }),
    );

    let waits: Promise<void>[] = [];
    act(() => {
      waits = [result.current.refresh(), result.current.cheapRefresh()];
    });
    await act(async () => {
      mount.resolve('mount');
      await Promise.all(waits);
    });

    expect(full).toHaveBeenCalledTimes(1);
    expect(cheap).not.toHaveBeenCalled();
    expect(result.current.data).toBe('full');
  });

  it('replaces a fetch older than the request budget instead of queueing behind it', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    try {
      const signals: AbortSignal[] = [];
      const fresh = deferred<string>();
      const fetcher = vi.fn((signal: AbortSignal) => {
        signals.push(signal);
        return signals.length === 1 ? new Promise<string>(() => {}) : fresh.promise;
      });

      const { result } = renderHook(() => useCachedData('hung', fetcher));
      vi.setSystemTime(Date.now() + 60_000);

      let refreshPromise: Promise<void> | undefined;
      act(() => {
        refreshPromise = result.current.refresh();
      });
      expect(fetcher).toHaveBeenCalledTimes(2);
      expect(signals[0]?.aborted).toBe(true);

      await act(async () => {
        fresh.resolve('recovered');
        await refreshPromise;
      });
      expect(result.current.data).toBe('recovered');
    } finally {
      vi.useRealTimers();
    }
  });

  it('releases a queued refresh when the hook unmounts', async () => {
    const { result, unmount } = renderHook(() =>
      useCachedData('queued-unmount', () => new Promise<string>(() => {})),
    );
    let refreshPromise: Promise<void> | undefined;
    act(() => {
      refreshPromise = result.current.refresh();
    });
    unmount();
    await expect(refreshPromise).resolves.toBeUndefined();
  });

  it('does not write a resolved fetch into the cache after unmount', async () => {
    const pending = deferred<string>();
    const cacheKey = 'unmounted';

    const { unmount } = renderHook(() => useCachedData(cacheKey, () => pending.promise));

    unmount();
    invalidate(cacheKey);

    await act(async () => {
      pending.resolve('late result');
      await pending.promise;
    });

    expect(getCached<string>(cacheKey)).toBeUndefined();
  });

  it('aborts the obsolete fetch signal when the cache key changes', () => {
    const signals: AbortSignal[] = [];
    const fetcher = (signal: AbortSignal) => {
      signals.push(signal);
      return new Promise<string>(() => {});
    };

    const { rerender } = renderHook(
      ({ cacheKey }: { cacheKey: string }) => useCachedData(cacheKey, fetcher),
      { initialProps: { cacheKey: 'first' } },
    );

    expect(signals).toHaveLength(1);
    expect(signals[0]?.aborted).toBe(false);

    rerender({ cacheKey: 'second' });

    expect(signals).toHaveLength(2);
    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);
  });

  it('aborts the active fetch signal on unmount', () => {
    let signal: AbortSignal | undefined;
    const { unmount } = renderHook(() =>
      useCachedData('unmounted-signal', (nextSignal: AbortSignal) => {
        signal = nextSignal;
        return new Promise<string>(() => {});
      }),
    );

    expect(signal?.aborted).toBe(false);
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it('exposes the cache fetch timestamp once data lands', async () => {
    const value = deferred<string>();
    const cacheKey = 'fetched-at-key';

    const { result } = renderHook(() => useCachedData(cacheKey, () => value.promise));

    // No write has happened yet, so there is no fetch timestamp.
    expect(result.current.fetchedAt).toBeUndefined();

    await act(async () => {
      value.resolve('landed');
      await value.promise;
    });
    await waitFor(() => expect(result.current.data).toBe('landed'));

    // The exposed timestamp mirrors the cache primitive the write stamped.
    expect(result.current.fetchedAt).toBe(getCachedFetchedAt(cacheKey));
    expect(result.current.fetchedAt).toEqual(expect.any(String));
  });

  it('seeds the fetch timestamp from the cache on mount', async () => {
    const cacheKey = 'preseeded-key';
    const { result } = renderHook(() => useCachedData(cacheKey, () => Promise.resolve('fresh')));
    await waitFor(() => expect(result.current.data).toBe('fresh'));
    const landedAt = result.current.fetchedAt;
    expect(landedAt).toEqual(expect.any(String));

    // A second mount on the warm cache seeds both data and timestamp
    // synchronously from cache; the never-settling refetch leaves them intact.
    const second = renderHook(() => useCachedData(cacheKey, () => new Promise<string>(() => {})));
    expect(second.result.current.data).toBe('fresh');
    expect(second.result.current.fetchedAt).toBe(landedAt);
  });

  it('cheapRefresh routes through sseRefreshFetcher when present', async () => {
    const primary = vi.fn(() => Promise.resolve('primary'));
    const wide = vi.fn(() => Promise.resolve('wide'));
    const cheap = vi.fn(() => Promise.resolve('cheap'));

    const { result } = renderHook(() =>
      useCachedData('runs:cheap', primary, {
        refreshFetcher: wide,
        sseRefreshFetcher: cheap,
      }),
    );

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.data).toBe('primary');

    await act(async () => {
      await result.current.cheapRefresh();
    });
    expect(cheap).toHaveBeenCalledTimes(1);
    expect(wide).not.toHaveBeenCalled();
    expect(result.current.data).toBe('cheap');

    await act(async () => {
      await result.current.refresh();
    });
    expect(wide).toHaveBeenCalledTimes(1);
    expect(result.current.data).toBe('wide');
  });

  it('cheapRefresh falls back to refreshFetcher then the primary fetcher', async () => {
    const primary = vi.fn(() => Promise.resolve('primary'));
    const wide = vi.fn(() => Promise.resolve('wide'));

    const withWide = renderHook(() =>
      useCachedData('runs:cheap-fallback-wide', primary, { refreshFetcher: wide }),
    );
    await waitFor(() => expect(withWide.result.current.loading).toBe(false));
    await act(async () => {
      await withWide.result.current.cheapRefresh();
    });
    expect(wide).toHaveBeenCalledTimes(1);

    const primaryOnly = vi.fn(() => Promise.resolve('primary-only'));
    const noOpts = renderHook(() => useCachedData('runs:cheap-fallback-primary', primaryOnly));
    await waitFor(() => expect(noOpts.result.current.loading).toBe(false));
    primaryOnly.mockClear();
    await act(async () => {
      await noOpts.result.current.cheapRefresh();
    });
    expect(primaryOnly).toHaveBeenCalledTimes(1);
  });

  it('reports the latest fetch failure through onError', async () => {
    const onError = vi.fn();
    const failure = new Error('network down');

    const { result } = renderHook(() =>
      useCachedData('broken', () => Promise.reject(failure), { onError }),
    );

    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(result.current.error).toBe('network down');
    expect(onError).toHaveBeenCalledWith(failure);
  });
});

function fetcherFor<T>(fetchers: Record<string, () => Promise<T>>, key: string) {
  const fetcher = fetchers[key];
  if (!fetcher) throw new Error(`missing fetcher for ${key}`);
  return fetcher;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}
