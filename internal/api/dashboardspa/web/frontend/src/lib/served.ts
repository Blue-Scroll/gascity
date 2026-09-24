import { useEffect, useState } from 'react';

// Is a route that only some machines provide actually here?
//
// The phone dashboard leans on helpers that sit beside gc, behind the same
// proxy: /upload for attachments, /pane and /keys for the live pane, /term/ for
// a browser terminal. A laptop running the plain supervisor has none of them.
// gc answers each of those paths with 404 (sidecarPaths in
// internal/api/dashboardspa/handler.go), so one request tells the page whether
// to offer the control. A control that is not served is not drawn, rather than
// drawn and left to fail when someone taps it.
//
// Each question is asked once per page load. The answer does not change while
// the page is open, and asking on every render would cost a request per render.
// A probe that throws counts as "not served".
const answers = new Map<string, Promise<boolean>>();

export function servedOnce(key: string, probe: () => Promise<boolean>): Promise<boolean> {
  let answer = answers.get(key);
  if (answer === undefined) {
    answer = probe().catch(() => false);
    answers.set(key, answer);
  }
  return answer;
}

// The same question from a component. `null` means the answer has not come back
// yet, which is different from "no": a page that has to say "not here" must not
// say it before it knows. Pass a probe defined at module level, not a new
// function each render.
export function useServed(key: string, probe: () => Promise<boolean>): boolean | null {
  const [served, setServed] = useState<boolean | null>(null);
  useEffect(() => {
    let live = true;
    void servedOnce(key, probe).then((ok) => {
      if (live) setServed(ok);
    });
    return () => {
      live = false;
    };
  }, [key, probe]);
  return served;
}

// Tests only: forget every answer, so each test asks afresh.
export function forgetServedAnswers(): void {
  answers.clear();
}
