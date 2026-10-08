import { useCallback, useEffect, useRef, useState } from 'react';
import { getCached, getCachedFetchedAt, setCached } from '../api/cache';
import { REQUEST_BUDGET_MS } from '../api/requestBudget';

interface UseCachedDataResult<T> {
  data: T | undefined;
  loading: boolean;
  error: string | null;
  /** ISO timestamp of the cache read backing `data`, or undefined before any lands. */
  fetchedAt: string | undefined;
  refresh: () => Promise<void>;
  /**
   * Routes through `sseRefreshFetcher` when configured, else falls back to the
   * primary refresh. A second, cheaper refresh path for high-frequency triggers
   * (e.g. SSE bursts) that should not pay the full refresh cost.
   */
  cheapRefresh: () => Promise<void>;
}

type CachedDataFetcher<T> = (signal: AbortSignal) => Promise<T>;

export interface UseCachedDataOptions<T> {
  /**
   * When provided, explicit refresh() calls route through this fetcher
   * instead of the primary one. The mount-effect always uses the
   * primary `fetcher` so initial paint stays cache-warm. Useful when
   * the same source has a cheap GET and a TTL-bypassing POST: pass
   * the GET as `fetcher` and the POST as `refreshFetcher`.
   */
  refreshFetcher?: CachedDataFetcher<T>;
  /**
   * When provided, `cheapRefresh()` routes through this fetcher instead of the
   * primary refresh path. Use for a cheaper variant of the same source that
   * high-frequency triggers (SSE bursts) prefer, while the manual/explicit
   * refresh stays on `refreshFetcher`. Falls back to `refreshFetcher` (or the
   * primary `fetcher`) when undefined, so existing callers are unaffected.
   */
  sseRefreshFetcher?: CachedDataFetcher<T>;
  onError?: (error: unknown) => void;
}

/** The fetch in flight. `runId` matches runIdRef while its result may land. */
interface ActiveFetch {
  runId: number;
  controller: AbortController;
  startedAt: number;
}

/** The one fetch queued behind the active one, and everyone waiting on it. */
interface QueuedFetch<T> {
  fetch: CachedDataFetcher<T>;
  /** A full refresh() asked for it. A cheapRefresh() never downgrades that. */
  full: boolean;
  done: Promise<void>;
  resolveDone: () => void;
}

/**
 * Stale-while-revalidate fetch hook. On mount:
 *   - If the cache has the key: seed state with cached data and
 *     render synchronously, then kick off a background refresh.
 *   - Otherwise: render loading=true and fetch.
 *
 * `key` changes (e.g. params shift) reseed from cache for the new
 * key and refetch. `fetcher` is captured in a ref so callers don't
 * need to memoize it to avoid refetch loops — refetches only fire
 * on key change or explicit refresh(). The fetcher receives a signal
 * that aborts when the key changes, the hook unmounts, or the fetch is
 * replaced as hung.
 *
 * One fetch runs at a time. A refresh that arrives while a fetch is in
 * flight does not abort it: it waits, and ONE more fetch runs after that
 * fetch lands, however many refreshes arrived meanwhile. Pages refresh on
 * every live event, every few seconds on a busy town, and some lists take
 * longer than that. When each refresh aborted the one before, a slow list
 * never landed at all, and the aborted requests kept running on the server
 * (hq-subxy4). The returned promise settles when the fetch that covers the
 * call has landed.
 *
 * A fetch older than REQUEST_BUDGET_MS counts as hung, so a refresh replaces
 * it instead of queueing behind something that may never settle.
 */
export function useCachedData<T>(
  key: string,
  fetcher: CachedDataFetcher<T>,
  options?: UseCachedDataOptions<T>,
): UseCachedDataResult<T> {
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;
  const refreshFetcherRef = useRef(options?.refreshFetcher);
  refreshFetcherRef.current = options?.refreshFetcher;
  const sseRefreshFetcherRef = useRef(options?.sseRefreshFetcher);
  sseRefreshFetcherRef.current = options?.sseRefreshFetcher;
  const onErrorRef = useRef(options?.onError);
  onErrorRef.current = options?.onError;
  const currentKeyRef = useRef(key);
  currentKeyRef.current = key;
  const runIdRef = useRef(0);
  const activeRef = useRef<ActiveFetch | null>(null);
  const queuedRef = useRef<QueuedFetch<T> | null>(null);

  const [data, setData] = useState<T | undefined>(() => getCached<T>(key));
  const [loading, setLoading] = useState<boolean>(() => getCached<T>(key) === undefined);
  const [error, setError] = useState<string | null>(null);
  const [fetchedAt, setFetchedAt] = useState<string | undefined>(() => getCachedFetchedAt(key));

  // Starts a fetch now, replacing (and aborting) any fetch still marked
  // active. Callers only do that for a hung fetch or a new key.
  const startFetch = useCallback(
    async (fetch: CachedDataFetcher<T>): Promise<void> => {
      const runId = runIdRef.current + 1;
      runIdRef.current = runId;
      activeRef.current?.controller.abort();
      const controller = new AbortController();
      activeRef.current = { runId, controller, startedAt: Date.now() };
      const cacheKey = key;
      setLoading(true);
      setError(null);
      try {
        const fresh = await fetch(controller.signal);
        if (runIdRef.current === runId && currentKeyRef.current === cacheKey) {
          setCached(cacheKey, fresh);
          setData(fresh);
          setFetchedAt(getCachedFetchedAt(cacheKey));
        }
      } catch (err) {
        if (runIdRef.current === runId) {
          setError(err instanceof Error ? err.message : 'failed to load');
          onErrorRef.current?.(err);
        }
      } finally {
        if (runIdRef.current === runId) {
          activeRef.current = null;
          setLoading(false);
          const queued = queuedRef.current;
          if (queued !== null) {
            queuedRef.current = null;
            void startFetch(queued.fetch).then(queued.resolveDone);
          }
        }
      }
    },
    [key],
  );

  const requestFetch = useCallback(
    (fetch: CachedDataFetcher<T>, full: boolean): Promise<void> => {
      const active = activeRef.current;
      if (active === null || Date.now() - active.startedAt >= REQUEST_BUDGET_MS) {
        return startFetch(fetch);
      }
      const queued = queuedRef.current;
      if (queued !== null) {
        // The latest ask wins, except that a cheap one never replaces a full one.
        if (full || !queued.full) {
          queued.fetch = fetch;
          queued.full = full;
        }
        return queued.done;
      }
      let resolveDone!: () => void;
      const done = new Promise<void>((resolve) => {
        resolveDone = resolve;
      });
      queuedRef.current = { fetch, full, done, resolveDone };
      return done;
    },
    [startFetch],
  );

  // Explicit refresh prefers the bypass fetcher when configured;
  // mount-effect always uses the cheap primary fetcher.
  const refresh = useCallback(
    () => requestFetch(refreshFetcherRef.current ?? fetcherRef.current, true),
    [requestFetch],
  );
  // Cheap refresh prefers the SSE fetcher, then the primary refresh fetcher,
  // then the cheap primary fetcher — so it is always a no-config superset.
  const cheapRefresh = useCallback(
    () =>
      requestFetch(
        sseRefreshFetcherRef.current ?? refreshFetcherRef.current ?? fetcherRef.current,
        false,
      ),
    [requestFetch],
  );

  useEffect(() => {
    const cached = getCached<T>(key);
    setData(cached);
    setLoading(cached === undefined);
    setFetchedAt(getCachedFetchedAt(key));
    void startFetch(fetcherRef.current);
    return () => {
      runIdRef.current += 1;
      activeRef.current?.controller.abort();
      activeRef.current = null;
      // Nothing will run the queued fetch for this key, so release its waiters.
      queuedRef.current?.resolveDone();
      queuedRef.current = null;
    };
  }, [key, startFetch]);

  return { data, loading, error, fetchedAt, refresh, cheapRefresh };
}
