package main

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/orders"
)

// The bug this file guards (hq-mb1gvr): the dispatch_orders phase took 20s to
// 221s a tick and its trace record carried no fields, so nobody could say which
// pass, which sweep or which order ate the time. dispatch now returns a report
// of where its time went, and the tick writes it onto that record.

func TestOrderDispatchReportCountsAndTimesEveryPass(t *testing.T) {
	store := beads.NewMemStore()
	// idle-order ran recently, so it is a candidate but not due.
	if _, err := store.Create(beads.Bead{
		Title:  "order run",
		Labels: []string{"order-run:idle-order"},
	}); err != nil {
		t.Fatal(err)
	}
	aa := []orders.Order{
		{
			Name:         "due-order",
			Trigger:      "cooldown",
			Interval:     "1m",
			Formula:      "test-formula",
			Pool:         "worker",
			FormulaLayer: sharedTestFormulaDir,
		},
		{
			Name:     "idle-order",
			Trigger:  "cooldown",
			Interval: "1h",
			Formula:  "test-formula",
		},
	}
	ad := buildOrderDispatcherFromList(aa, store, nil)
	if ad == nil {
		t.Fatal("expected non-nil dispatcher")
	}

	report := ad.dispatch(context.Background(), t.TempDir(), time.Now())
	ad.drain(context.Background())

	if report.orders != 2 || report.candidates != 2 || report.due != 1 || report.fired != 1 {
		t.Fatalf("counts = orders %d, candidates %d, due %d, fired %d; want 2, 2, 1, 1",
			report.orders, report.candidates, report.due, report.fired)
	}
	if report.gateTimeouts != 0 {
		t.Fatalf("gateTimeouts = %d, want 0", report.gateTimeouts)
	}
	for _, name := range []string{"due-order", "idle-order"} {
		if _, ok := report.orderCost[name]; !ok {
			t.Fatalf("orderCost has no entry for %s: %v", name, report.orderCost)
		}
	}

	fields := tickPhaseParts{}
	report.addTo(fields)
	for _, key := range []string{
		"orders", "candidates", "due", "fired", "gate_timeouts", "slowest_orders",
		"gates_ms", "gates_bd_calls",
		"condition_checks_ms", "condition_checks_bd_calls",
		"due_pass_ms", "due_pass_bd_calls",
		"launch_ms", "launch_bd_calls",
	} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("dispatch_orders fields lack %s: %#v", key, fields)
		}
	}
}

func TestOrderDispatchReportOfAnEmptyDispatcherIsZero(t *testing.T) {
	m := &memoryOrderDispatcher{}
	report := m.dispatch(context.Background(), t.TempDir(), time.Now())
	if report.orders != 0 || report.fired != 0 || report.slowestOrders() != "" {
		t.Fatalf("report = %+v, want zero counts and no slowest orders", report)
	}
	fields := tickPhaseParts{}
	report.addTo(fields)
	if _, ok := fields["slowest_orders"]; ok {
		t.Fatalf("an empty report wrote slowest_orders: %#v", fields)
	}
}

func TestOrderDispatchReportNamesTheSlowestOrdersFirst(t *testing.T) {
	report := orderDispatchReport{orderCost: map[string]time.Duration{
		"fast":    5 * time.Millisecond,
		"slow-b":  200 * time.Millisecond,
		"slowest": 300 * time.Millisecond,
		"slow-a":  200 * time.Millisecond,
	}}
	// Ties sort by name so the field reads the same on every tick.
	want := "slowest=300ms,slow-a=200ms,slow-b=200ms"
	if got := report.slowestOrders(); got != want {
		t.Fatalf("slowestOrders() = %q, want %q", got, want)
	}
}

// An iteration that ends in `continue` never reaches code at the bottom of the
// loop. The clock charges it anyway, because the next iteration's next() or
// the stop() after the loop settles it.
func TestOrderCostClockChargesEveryIteration(t *testing.T) {
	cost := map[string]time.Duration{}
	clock := &orderCostClock{cost: cost}
	for _, name := range []string{"a", "b", "a"} {
		clock.next(name)
		if name == "b" {
			continue
		}
	}
	clock.stop()
	clock.stop() // a second stop charges nothing twice

	if len(cost) != 2 {
		t.Fatalf("cost = %v, want entries for a and b", cost)
	}
	for name, d := range cost {
		if d < 0 {
			t.Fatalf("cost[%s] = %v, want >= 0", name, d)
		}
	}
}
