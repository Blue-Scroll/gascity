package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// startBeadScopedPoolSession starts a pool worker the way the reconciler
// names it since #6549 (<basename(pool)>-<session bead id>) and sets the env
// that says which member it is.
func startBeadScopedPoolSession(t *testing.T, state *fakeState, name, pool, alias string) {
	t.Helper()
	if err := state.sp.Start(context.Background(), name, runtime.Config{}); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	for key, value := range map[string]string{
		"GC_TEMPLATE":   pool,
		"GC_ALIAS":      alias,
		"GC_SESSION_ID": name[len("polecat-"):],
	} {
		if err := state.sp.SetMeta(name, key, value); err != nil {
			t.Fatalf("set %s on %s: %v", key, name, err)
		}
	}
}

// vn-kpk61ob: the town's polecats run as "polecat-opus-high-hq-wisp-…", so
// the list derived "vessel-network--dune", found nothing, and showed 4 of 26
// live agents.
func TestAgentListFindsBoundedPoolMemberUnderBeadScopedName(t *testing.T) {
	state := newFakeState(t)
	state.cfg.Agents = []config.Agent{
		{
			Name: "polecat", Dir: "myrig",
			MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3),
			NamepoolNames: []string{"alpha", "bravo", "charlie"},
		},
		{
			Name: "polecat", Dir: "otherrig",
			MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3),
			NamepoolNames: []string{"alpha", "bravo", "charlie"},
		},
	}
	startBeadScopedPoolSession(t, state, "polecat-hq-wisp-bravo", "myrig/polecat", "myrig/bravo")
	// Same base name, other rig: both pools' sessions start "polecat-", so
	// only the env says whose each one is.
	startBeadScopedPoolSession(t, state, "polecat-hq-wisp-ocharlie", "otherrig/polecat", "otherrig/charlie")
	// A session that names a member of another pool must not stand in for
	// it, nor add a second myrig/charlie row.
	startBeadScopedPoolSession(t, state, "polecat-hq-wisp-stray", "otherrig/polecat", "myrig/charlie")

	list := getAgentList(t, state, "")
	items := agentRowsByName(list.Items)
	charlies := 0
	for _, item := range list.Items {
		if item.Name == "myrig/charlie" {
			charlies++
		}
	}
	if charlies != 1 {
		t.Errorf("myrig/charlie rows = %d, want 1", charlies)
	}
	if other := items["otherrig/charlie"]; !other.Running || other.Session == nil || other.Session.Name != "polecat-hq-wisp-ocharlie" {
		t.Errorf("otherrig/charlie = running %v, session %+v; want running on polecat-hq-wisp-ocharlie", other.Running, other.Session)
	}

	bravo, ok := items["myrig/bravo"]
	if !ok {
		t.Fatalf("no row for myrig/bravo in %v", items)
	}
	if !bravo.Running {
		t.Errorf("myrig/bravo Running = false, want true")
	}
	if bravo.Session == nil || bravo.Session.Name != "polecat-hq-wisp-bravo" || bravo.Session.ID != "hq-wisp-bravo" {
		t.Errorf("myrig/bravo Session = %+v, want name polecat-hq-wisp-bravo, id hq-wisp-bravo", bravo.Session)
	}
	for _, name := range []string{"myrig/alpha", "myrig/charlie"} {
		if items[name].Running {
			t.Errorf("%s Running = true, want false", name)
		}
	}
}

func TestAgentGetFindsPoolMemberUnderBeadScopedName(t *testing.T) {
	state := newFakeState(t)
	state.cfg.Agents = []config.Agent{
		{
			Name: "polecat", Dir: "myrig",
			MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3),
			NamepoolNames: []string{"alpha", "bravo", "charlie"},
		},
	}
	startBeadScopedPoolSession(t, state, "polecat-hq-wisp-bravo", "myrig/polecat", "myrig/bravo")
	h := newTestCityHandlerWith(t, state, New(state))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", cityURL(state, "/agent/myrig/bravo"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got agentResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Running || got.Session == nil || got.Session.Name != "polecat-hq-wisp-bravo" {
		t.Errorf("GET myrig/bravo = running %v, session %+v; want running on polecat-hq-wisp-bravo", got.Running, got.Session)
	}
}

// An unlimited pool's discovery matches derived names only, so a live member
// under a bead-scoped name had no row at all.
func TestAgentListAddsUnlimitedPoolMemberUnderBeadScopedName(t *testing.T) {
	state := newFakeState(t)
	state.cfg.Agents = []config.Agent{
		{
			Name: "polecat", Dir: "myrig",
			MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(-1),
		},
	}
	startBeadScopedPoolSession(t, state, "polecat-hq-wisp-seven", "myrig/polecat", "myrig/polecat-7")

	items := agentRowsByName(getAgentList(t, state, "").Items)

	row, ok := items["myrig/polecat-7"]
	if !ok {
		t.Fatalf("no row for myrig/polecat-7 in %v", items)
	}
	if !row.Running || row.Pool != "myrig/polecat" {
		t.Errorf("myrig/polecat-7 = running %v, pool %q; want running in myrig/polecat", row.Running, row.Pool)
	}
	if _, idle := items["myrig/polecat"]; idle {
		t.Errorf("the idle pool row is listed beside a live member")
	}
}

func TestBeadScopedPoolPrefixMatchesPoolSessionName(t *testing.T) {
	// PoolSessionName in cmd/gc builds "<basename(template)>-<bead id>".
	if got := beadScopedPoolPrefix("vessel-network/polecat-opus-high"); got != "polecat-opus-high-" {
		t.Errorf("beadScopedPoolPrefix = %q, want %q", got, "polecat-opus-high-")
	}
}
