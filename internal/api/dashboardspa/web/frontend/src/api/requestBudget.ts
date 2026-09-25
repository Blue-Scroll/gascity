// How long the dashboard gives one request before it calls it failed. The
// supervisor client times a request out after this, and useCachedData treats a
// fetch older than this as hung, so a later refresh may replace it. One number,
// so the two never disagree. It lives in its own module so a test that mocks
// the supervisor client still leaves it in place.
export const REQUEST_BUDGET_MS = 60_000;
