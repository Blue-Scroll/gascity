package api

import (
	"strconv"

	"github.com/gastownhall/gascity/internal/api/genclient"
	"github.com/gastownhall/gascity/internal/orders"
)

// OrderHistoryView is the CLI-facing shape for `gc order history` rows. It
// mirrors the subset of fields the CLI formatter reads so cmd/gc/ never
// imports genclient directly.
type OrderHistoryView struct {
	BeadID     string
	Name       string
	ScopedName string
	Rig        string
	CreatedAt  string
	// Exec is how the run's exec command ended, or nil when the run recorded
	// none (a wisp run, an in-flight run, an older bead).
	Exec *orders.ExecResult
}

// orderHistoryViewFromGen translates one genclient.OrderHistoryEntry into an
// OrderHistoryView. Optional pointer fields are dereferenced safely.
func orderHistoryViewFromGen(g genclient.OrderHistoryEntry) OrderHistoryView {
	out := OrderHistoryView{
		BeadID:     g.BeadId,
		Name:       g.Name,
		ScopedName: g.ScopedName,
		CreatedAt:  g.CreatedAt,
	}
	if g.Rig != nil {
		out.Rig = *g.Rig
	}
	out.Exec = execResultFromGen(g)
	return out
}

// execResultFromGen decodes an entry's exec result through the same codec the
// server encoded it with. timed_out is the marker: the server sets it on every
// exec run with a recorded result and on nothing else, so duration_ms alone
// (which a convergence gate also writes) is not read as an exec run.
func execResultFromGen(g genclient.OrderHistoryEntry) *orders.ExecResult {
	if g.TimedOut == nil {
		return nil
	}
	meta := map[string]string{orders.MetaExecTimedOut: strconv.FormatBool(*g.TimedOut)}
	for key, value := range map[string]*string{
		orders.MetaExecDurationMs: g.DurationMs,
		orders.MetaExecTimeoutMs:  g.TimeoutMs,
		orders.MetaExecExitCode:   g.ExitCode,
	} {
		if value != nil {
			meta[key] = *value
		}
	}
	result, ok := orders.ExecResultFromMetadata(meta)
	if !ok {
		return nil
	}
	return &result
}

// orderHistoryFromGenList translates the genclient list body into
// []OrderHistoryView. Returns an empty slice (never nil) when the body is
// missing or holds no entries so callers can uniformly format the empty case.
func orderHistoryFromGenList(body *genclient.OrderHistoryListBody) []OrderHistoryView {
	if body == nil || body.Entries == nil {
		return []OrderHistoryView{}
	}
	items := *body.Entries
	out := make([]OrderHistoryView, 0, len(items))
	for _, item := range items {
		out = append(out, orderHistoryViewFromGen(item))
	}
	return out
}
