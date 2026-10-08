import { cleanup, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PARTIAL_REFRESH_MS, useRefreshWhilePartial } from './useRefreshWhilePartial';

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function renderRefresh(initial: { partial: boolean; fetchedAt: string | undefined }) {
  const refresh = vi.fn(() => Promise.resolve());
  const view = renderHook(
    ({ partial, fetchedAt }: { partial: boolean; fetchedAt: string | undefined }) =>
      useRefreshWhilePartial(partial, fetchedAt, refresh),
    { initialProps: initial },
  );
  return { refresh, ...view };
}

describe('useRefreshWhilePartial', () => {
  it('asks again once after a partial answer lands', () => {
    const { refresh } = renderRefresh({ partial: true, fetchedAt: 't1' });

    vi.advanceTimersByTime(PARTIAL_REFRESH_MS - 1);
    expect(refresh).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(refresh).toHaveBeenCalledTimes(1);

    // The same answer never schedules a second ask.
    vi.advanceTimersByTime(PARTIAL_REFRESH_MS * 3);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('asks again for each new partial answer', () => {
    const { refresh, rerender } = renderRefresh({ partial: true, fetchedAt: 't1' });
    vi.advanceTimersByTime(PARTIAL_REFRESH_MS);

    rerender({ partial: true, fetchedAt: 't2' });
    vi.advanceTimersByTime(PARTIAL_REFRESH_MS);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it('stops asking once a full answer lands', () => {
    const { refresh, rerender } = renderRefresh({ partial: true, fetchedAt: 't1' });

    rerender({ partial: false, fetchedAt: 't2' });
    vi.advanceTimersByTime(PARTIAL_REFRESH_MS * 3);
    expect(refresh).not.toHaveBeenCalled();
  });

  it('never asks for a full answer', () => {
    const { refresh } = renderRefresh({ partial: false, fetchedAt: 't1' });

    vi.advanceTimersByTime(PARTIAL_REFRESH_MS * 3);
    expect(refresh).not.toHaveBeenCalled();
  });
});
