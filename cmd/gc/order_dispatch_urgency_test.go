package main

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/orders"
)

// The bug this file guards (hq-ggxthx): the dispatch budget is smaller than the
// number of orders a busy city has due at once, and it used to be spent by
// rotation position. power-watch, the battery fuse that pages Casey at 40%,
// asks for 5 minutes and went 17.4 waiting its turn behind housekeeping orders
// that ask for 24 hours. Nothing went red, because an order reports success
// whenever it finally runs.

func TestOrderDispatchUrgencyMeasuresLatenessInTheOrdersOwnInterval(t *testing.T) {
	now := time.Date(2026, 9, 22, 4, 10, 0, 0, time.UTC)
	cooldown := func(interval string) orders.Order {
		return orders.Order{Name: "o", Trigger: "cooldown", Interval: interval, Exec: "true"}
	}

	cases := []struct {
		name    string
		order   orders.Order
		lastRun time.Time
		want    float64
	}{
		{
			// The measured incident: a 5m fuse that had not run for 17.4m.
			name:    "the 5m fuse that went 17.4m",
			order:   cooldown("5m"),
			lastRun: now.Add(-17*time.Minute - 24*time.Second),
			want:    3.48,
		},
		{
			// The order it was queued behind. An hour late on a daily order is
			// barely late at all, and the score says so.
			name:    "a 24h order an hour past due",
			order:   cooldown("24h"),
			lastRun: now.Add(-25 * time.Hour),
			want:    25.0 / 24.0,
		},
		{
			name:    "due this instant scores the baseline",
			order:   cooldown("5m"),
			lastRun: now.Add(-5 * time.Minute),
			want:    orderDispatchUrgencyBaseline,
		},
		{
			name:    "a condition order declares no period",
			order:   orders.Order{Name: "o", Trigger: "condition", Check: "true", Exec: "true"},
			lastRun: now.Add(-9 * time.Hour),
			want:    orderDispatchUrgencyBaseline,
		},
		{
			// A cron order is quiet for a day and then due. Its quiet spell is
			// not lateness, and reading it as lateness would let one daily
			// report outrank every safety watch in the city.
			name:    "a cron order declares no period",
			order:   orders.Order{Name: "o", Trigger: "cron", Schedule: "0 4 * * *", Exec: "true"},
			lastRun: now.Add(-24 * time.Hour),
			want:    orderDispatchUrgencyBaseline,
		},
		{
			name:    "an unparseable interval is no period",
			order:   cooldown("every-so-often"),
			lastRun: now.Add(-9 * time.Hour),
			want:    orderDispatchUrgencyBaseline,
		},
		{
			// A clock that moved backwards must not sink a due order below the
			// orders that declare no period at all.
			name:    "a backwards clock falls back to the baseline",
			order:   cooldown("5m"),
			lastRun: now.Add(time.Minute),
			want:    orderDispatchUrgencyBaseline,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := orderDispatchUrgency(tc.order, tc.lastRun, now)
			if math.Abs(got-tc.want) > 0.01 {
				t.Fatalf("urgency = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOrderDispatchUrgencyRanksANeverRunOrderFirst(t *testing.T) {
	now := time.Date(2026, 9, 22, 4, 10, 0, 0, time.UTC)
	order := orders.Order{Name: "o", Trigger: "cooldown", Interval: "5m", Exec: "true"}
	got := orderDispatchUrgency(order, time.Time{}, now)
	if !math.IsInf(got, 1) {
		t.Fatalf("never-run urgency = %v, want +Inf", got)
	}
}

// The regression test for the incident. The fuse is LAST in the order list, so
// rotation position hands the tick's one slot to a housekeeping order and the
// fuse waits. Lateness against its own interval hands the slot to the fuse.
//
// Every clock reading here is anchored to the run the dispatcher actually
// recorded, never to a fixed date: a tracking bead is stamped with the store's
// own clock, so a test that mixes a fabricated `now` with a real CreatedAt
// measures the gap between the two and starts failing on a chosen day.
func TestOrderDispatchBudgetGoesToTheOrderWhoseScheduleIsMostBroken(t *testing.T) {
	store := beads.NewMemStore()
	var aa []orders.Order
	for i := 0; i < 4; i++ {
		aa = append(aa, orders.Order{
			Name:     fmt.Sprintf("housekeeping-%d", i),
			Trigger:  "cooldown",
			Interval: "17m",
			Exec:     "true",
		})
	}
	aa = append(aa, orders.Order{
		Name:     "power-watch",
		Trigger:  "cooldown",
		Interval: "5m",
		Exec:     "true",
	})

	ad := buildOrderDispatcherFromListExec(aa, store, nil, func(context.Context, string, string, []string) ([]byte, error) {
		return []byte("ok\n"), nil
	}, nil)
	if ad == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	m := ad.(*memoryOrderDispatcher)

	// A budget wide enough for everything, so all five orders share one
	// starting line and the second tick is the only contested one.
	m.maxDispatchesPerTick = len(aa)
	ad.dispatch(context.Background(), t.TempDir(), time.Now())
	ad.drain(context.Background())
	firstRun := onlyTrackingRunTime(t, store, "power-watch")

	// 18 minutes on, every order is due: the housekeeping orders by a minute,
	// the fuse by 13. One slot. It belongs to the fuse.
	m.maxDispatchesPerTick = 1
	ad.dispatch(context.Background(), t.TempDir(), firstRun.Add(18*time.Minute))
	ad.drain(context.Background())

	if got := len(trackingBeads(t, store, "order-run:power-watch")); got != 2 {
		t.Fatalf("power-watch runs = %d, want 2 (it did not win the contested slot)", got)
	}
	for i := 0; i < 4; i++ {
		label := fmt.Sprintf("order-run:housekeeping-%d", i)
		if got := len(trackingBeads(t, store, label)); got != 1 {
			t.Fatalf("%s runs = %d, want 1 (it took the slot the fuse needed)", label, got)
		}
	}
}

// Nothing may starve. An order that declares no period has only its turn, so a
// city whose short cooldown orders want every tick must still get to it.
//
// This drives the decision directly rather than through a store, because a
// tracking bead is stamped with the store's own clock: a loop that advances a
// fabricated `now` while the recorded run times stand still measures the gap
// between two clocks instead of the scheduler.
func TestOrderDispatchMissedTurnsLiftAnOrderThatDeclaresNoPeriod(t *testing.T) {
	m := &memoryOrderDispatcher{maxDispatchesPerTick: 1}
	tight := orders.Order{Name: "tight", Trigger: "cooldown", Interval: "30s", Exec: "true"}
	noPeriod := orders.Order{Name: "no-period", Trigger: "condition", Check: "true", Exec: "true"}

	// A tick every 35 seconds against a 30-second interval: tight is 1.17x
	// overdue on every single tick, so on lateness alone it takes the one slot
	// forever and no-period never runs.
	const tick = 35 * time.Second
	now := time.Date(2026, 9, 22, 4, 0, 0, 0, time.UTC)
	tightLastRun := now.Add(-tick)

	noPeriodRuns := 0
	for i := 0; i < 6; i++ {
		due := []*orderDispatchCandidate{
			{idx: 0, order: noPeriod, scoped: noPeriod.Name},
			{idx: 1, order: tight, scoped: tight.Name},
		}
		due[0].urgency = m.scoreDispatchUrgency(noPeriod, noPeriod.Name, time.Time{}, now)
		due[1].urgency = m.scoreDispatchUrgency(tight, tight.Name, tightLastRun, now)

		fires := m.pickDispatchBudget(due, len(due))
		if len(fires) != 1 {
			t.Fatalf("tick %d fired %d orders, want 1", i, len(fires))
		}
		if fires[0].scoped == tight.Name {
			tightLastRun = now
		} else {
			noPeriodRuns++
		}
		now = now.Add(tick)
	}

	if noPeriodRuns == 0 {
		t.Fatal("no-period never won a slot: an order that declares no period was starved")
	}
}

// The rotation cursor must land on the first order the tick could not reach, so
// that orders which tie keep taking turns.
func TestOrderDispatchRotationResumesAtTheFirstOrderItCouldNotReach(t *testing.T) {
	store := beads.NewMemStore()
	var aa []orders.Order
	for i := 0; i < 4; i++ {
		aa = append(aa, orders.Order{
			Name:    fmt.Sprintf("condition-%d", i),
			Trigger: "condition",
			Check:   "true",
			Exec:    "true",
		})
	}
	ad := buildOrderDispatcherFromListExec(aa, store, nil, func(context.Context, string, string, []string) ([]byte, error) {
		return []byte("ok\n"), nil
	}, nil)
	if ad == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	m := ad.(*memoryOrderDispatcher)
	m.maxDispatchesPerTick = 2

	ad.dispatch(context.Background(), t.TempDir(), time.Now())
	ad.drain(context.Background())

	if m.nextDispatchStart != 2 {
		t.Fatalf("nextDispatchStart = %d, want 2 (the first order the tick could not reach)", m.nextDispatchStart)
	}
	for _, name := range []string{"condition-0", "condition-1"} {
		if got := len(trackingBeads(t, store, "order-run:"+name)); got != 1 {
			t.Fatalf("%s runs = %d, want 1", name, got)
		}
	}
	for _, name := range []string{"condition-2", "condition-3"} {
		if got := len(trackingBeads(t, store, "order-run:"+name)); got != 0 {
			t.Fatalf("%s runs = %d, want 0", name, got)
		}
	}
}

// onlyTrackingRunTime returns the run time the dispatcher recorded for an order
// that has run exactly once.
func onlyTrackingRunTime(t *testing.T, store beads.Store, orderName string) time.Time {
	t.Helper()
	runs := trackingBeads(t, store, "order-run:"+orderName)
	if len(runs) != 1 {
		t.Fatalf("%s tracking runs = %d, want 1", orderName, len(runs))
	}
	return runs[0].CreatedAt
}
