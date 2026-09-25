import { useEffect } from 'react';

/** How long a page waits before it asks again for a list the server marked partial. */
export const PARTIAL_REFRESH_MS = 3_000;

/**
 * Asks for a list again, once per answer that lands, while the server says
 * that answer is partial.
 *
 * GET /agents and GET /sessions answer within a second even while the bead
 * store is slow: from live names, or from the last full list, marked partial.
 * The full read keeps going on the server, and asking again is how its details
 * reach the page, even when no live event comes along to trigger a refresh
 * (vn-fzant5y).
 *
 * `fetchedAt` changes each time an answer lands, so each partial answer
 * schedules exactly one more ask. A failed ask lands nothing and schedules
 * nothing, so a dead server is not polled in a loop.
 */
export function useRefreshWhilePartial(
  partial: boolean,
  fetchedAt: string | undefined,
  refresh: () => Promise<void>,
): void {
  useEffect(() => {
    if (!partial) return undefined;
    const timer = setTimeout(() => void refresh(), PARTIAL_REFRESH_MS);
    return () => clearTimeout(timer);
  }, [partial, fetchedAt, refresh]);
}
