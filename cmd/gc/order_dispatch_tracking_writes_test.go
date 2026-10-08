package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/orders"
)

// barrierCreateStore holds every order-tracking Create until `want` of them
// are waiting at once, or until `wait` runs out. A dispatcher that writes its
// tracking beads one after another can only ever have one waiting, so every
// write times out and the test sees it. Other creates pass straight through.
type barrierCreateStore struct {
	beads.Store

	want int
	wait time.Duration

	mu       sync.Mutex
	waiting  int
	released chan struct{}
	timedOut int
}

func newBarrierCreateStore(want int, wait time.Duration) *barrierCreateStore {
	return &barrierCreateStore{
		Store:    beads.NewMemStore(),
		want:     want,
		wait:     wait,
		released: make(chan struct{}),
	}
}

func (s *barrierCreateStore) Create(b beads.Bead) (beads.Bead, error) {
	if !strings.HasPrefix(b.Title, "order:") {
		return s.Store.Create(b)
	}
	s.mu.Lock()
	s.waiting++
	if s.waiting == s.want {
		close(s.released)
	}
	s.mu.Unlock()
	select {
	case <-s.released:
	case <-time.After(s.wait):
		s.mu.Lock()
		s.timedOut++
		s.mu.Unlock()
	}
	return s.Store.Create(b)
}

func (s *barrierCreateStore) timeouts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timedOut
}

func dueCooldownOrders(n int) []orders.Order {
	aa := make([]orders.Order, 0, n)
	for i := 0; i < n; i++ {
		aa = append(aa, orders.Order{
			Name:     fmt.Sprintf("order-%d", i),
			Trigger:  "cooldown",
			Interval: "1m",
			Exec:     "true",
		})
	}
	return aa
}

// TestOrderDispatchWritesTrackingBeadsConcurrently pins hq-xe7ohr: a tick's
// tracking-bead writes run at the same time, so a tick costs one write
// latency, not one per fired order.
func TestOrderDispatchWritesTrackingBeadsConcurrently(t *testing.T) {
	const fires = 4
	store := newBarrierCreateStore(fires, 5*time.Second)
	ad := buildOrderDispatcherFromListExec(dueCooldownOrders(fires), store, nil, successfulExec, nil)
	m := ad.(*memoryOrderDispatcher)
	m.maxDispatchesPerTick = fires

	now := time.Date(2026, 9, 16, 2, 0, 0, 0, time.UTC)
	m.dispatch(context.Background(), t.TempDir(), now)
	defer m.drain(context.Background())

	if got := store.timeouts(); got != 0 {
		t.Fatalf("%d tracking writes waited out the barrier; the tick wrote them one after another", got)
	}
	// dispatch must not return before every write landed: the next tick's
	// open-tracking gate reads the store, so a bead still being written
	// would let the order fire twice.
	if got := countOrderTrackingRuns(t, store); got != fires {
		t.Fatalf("tracking beads when dispatch returned = %d, want %d", got, fires)
	}
}

// TestOrderDispatchSerialWritesAgree proves the concurrent write path and a
// one-at-a-time path fire the same orders.
func TestOrderDispatchSerialWritesAgree(t *testing.T) {
	prev := orderTrackingWriteConcurrency
	orderTrackingWriteConcurrency = 1
	t.Cleanup(func() { orderTrackingWriteConcurrency = prev })

	store := beads.NewMemStore()
	ad := buildOrderDispatcherFromListExec(dueCooldownOrders(5), store, nil, successfulExec, nil)
	m := ad.(*memoryOrderDispatcher)
	m.maxDispatchesPerTick = 0

	now := time.Date(2026, 9, 16, 2, 0, 0, 0, time.UTC)
	m.dispatch(context.Background(), t.TempDir(), now)
	m.drain(context.Background())
	if got := countOrderTrackingRuns(t, store); got != 5 {
		t.Fatalf("tracking runs = %d, want 5", got)
	}
}

// TestOrderDispatchPicksEachOrderOncePerTick: the gates read the store as it
// was before the tick wrote anything, so an order listed twice would pass
// both times. The fire loop must still write only one tracking bead for it.
func TestOrderDispatchPicksEachOrderOncePerTick(t *testing.T) {
	store := beads.NewMemStore()
	a := orders.Order{Name: "twice", Trigger: "cooldown", Interval: "1m", Exec: "true"}
	ad := buildOrderDispatcherFromListExec([]orders.Order{a, a}, store, nil, successfulExec, nil)
	m := ad.(*memoryOrderDispatcher)

	now := time.Date(2026, 9, 16, 2, 0, 0, 0, time.UTC)
	m.dispatch(context.Background(), t.TempDir(), now)
	m.drain(context.Background())
	if got := len(trackingBeads(t, store, "order-run:twice")); got != 1 {
		t.Fatalf("tracking beads for an order listed twice = %d, want 1", got)
	}
}

// TestOrderDispatchDefaultBudgetFiresEveryDueOrder: at the default budget a
// tick fires every due order of a normal-sized wave, not 4 of them.
func TestOrderDispatchDefaultBudgetFiresEveryDueOrder(t *testing.T) {
	const due = 10
	if due > defaultMaxOrderDispatchesPerTick {
		t.Fatalf("test wave %d is larger than the default budget %d", due, defaultMaxOrderDispatchesPerTick)
	}
	store := beads.NewMemStore()
	ad := buildOrderDispatcherFromListExec(dueCooldownOrders(due), store, nil, successfulExec, nil)
	m := ad.(*memoryOrderDispatcher)

	now := time.Date(2026, 9, 16, 2, 0, 0, 0, time.UTC)
	m.dispatch(context.Background(), t.TempDir(), now)
	m.drain(context.Background())
	if got := countOrderTrackingRuns(t, store); got != due {
		t.Fatalf("tracking runs after one default-budget tick = %d, want %d", got, due)
	}
}
