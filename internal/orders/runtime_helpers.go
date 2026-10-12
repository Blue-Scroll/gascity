package orders

import (
	"log"
	"time"

	"github.com/gastownhall/gascity/internal/events"
)

var runtimeHelpersLogf = log.Printf

// LastRunAcross returns a LastRunFunc reporting the most recent run time for a
// named order across a federation of order front doors (the dispatcher/CLI
// city + rig scopes). Each *Store performs its own MIXED orders+graph LastRun
// read (unioning its orders leg with its graph leg); the max across scopes wins.
// A per-scope error aborts and propagates. nil entries are skipped.
func LastRunAcross(stores []*Store) LastRunFunc {
	return func(name string) (time.Time, error) {
		var latest time.Time
		for _, s := range stores {
			if s == nil {
				continue
			}
			last, err := s.LastRun(name)
			if err != nil {
				return time.Time{}, err
			}
			if last.After(latest) {
				latest = last
			}
		}
		return latest, nil
	}
}

// lastRunEventWindowFloor is the shortest look-back LastRunEventWindow hands
// the order.fired fallback. A week plus a day covers every daily and weekly
// cron slot. On the town Mac it is the active log plus two or three archives,
// out of the 43 (1.3 GB of gzip) retention keeps.
const lastRunEventWindowFloor = 8 * 24 * time.Hour

// LastRunEventWindow is how far back the order.fired fallback
// (LastRunFuncWithEventFallback) looks for this order's last run. A run older
// than the window reads as "never run", which gives the same verdict:
//
//   - cooldown: the window is at least the interval, and a run older than the
//     interval is due exactly like a run that never happened.
//   - cron: a never-run order catches up over the last 25h (checkCron), so a
//     daily or weekly schedule fires the same either way. The one difference:
//     a schedule slower than the window whose tracking bead was pruned AND
//     that missed a slot older than 25h skips that one catch-up.
//
// Without a window, an order with no order.fired on record (one a hot reload
// just added) gunzipped and decoded every retained archive. At load 90 that
// stalled order dispatch for 1h50m on 2026-10-12 (vn-ngdqasv).
func (a Order) LastRunEventWindow() time.Duration {
	window := lastRunEventWindowFloor
	if a.Trigger == "cooldown" {
		if interval, err := time.ParseDuration(a.Interval); err == nil && interval > window {
			window = interval
		}
	}
	return window
}

// LastRunFuncWithEventFallback wraps fn with an events.Provider fallback.
// When fn returns a zero time (no tracking beads found — e.g. after a
// wisp-compact prune), it queries the event bus for the most recent
// order.fired event at or after since whose Subject matches the order's
// scoped name and returns its timestamp. A nil ep disables the fallback and
// the wrapped fn's result is returned unchanged. Event bus errors are
// non-fatal: when the provider fails the zero time is returned so the order
// appears unrun rather than permanently blocked.
//
// since bounds the read: archives rotated before it are never opened. Pass
// now minus the order's LastRunEventWindow. A zero since reads the whole
// retained history, which is the 2026-10-12 stall (vn-ngdqasv).
func LastRunFuncWithEventFallback(fn LastRunFunc, ep events.Provider, since time.Time) LastRunFunc {
	if ep == nil {
		return fn
	}
	return func(name string) (time.Time, error) {
		last, err := fn(name)
		if err != nil {
			return time.Time{}, err
		}
		if !last.IsZero() {
			return last, nil
		}
		evts, listErr := ep.List(events.Filter{
			Type:    events.OrderFired,
			Subject: name,
			Since:   since,
		})
		if listErr != nil {
			return time.Time{}, nil
		}
		var latest time.Time
		for _, e := range evts {
			if e.Ts.After(latest) {
				latest = e.Ts
			}
		}
		return latest, nil
	}
}

// CursorAcross returns a CursorFunc merging the event seq cursor for a named
// order across a federation of order front doors. Each *Store performs its own
// MIXED orders+graph Cursor read; the max seq across scopes wins. nil entries
// are skipped.
func CursorAcross(stores []*Store) CursorFunc {
	return func(name string) uint64 {
		var latest uint64
		for _, s := range stores {
			if s == nil {
				continue
			}
			if seq := uint64(s.Cursor(name)); seq > latest {
				latest = seq
			}
		}
		return latest
	}
}
