package main

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"
)

// reportingOrderDispatcher is an order dispatcher that answers with a fixed
// report, so a test can see what the lane does with one.
type reportingOrderDispatcher struct {
	report orderDispatchReport
}

func (d *reportingOrderDispatcher) dispatch(context.Context, string, time.Time) orderDispatchReport {
	return d.report
}

func (d *reportingOrderDispatcher) drain(context.Context) bool { return true }

// The bug this guards (hq-mb1gvr): the dispatch_orders record carried no
// fields, so a 20s to 221s phase could not say which of its parts was slow.
// The lane runs the body now, and its record must carry the parts the tick's
// record did: each part of the body as <part>_ms and <part>_bd_calls, the
// dispatcher's own passes and counts, and the slowest orders.
func TestOrdersLanePassRecordsWhereDispatchOrdersSpentItsTime(t *testing.T) {
	od := &reportingOrderDispatcher{report: orderDispatchReport{
		orders:       7,
		candidates:   5,
		due:          3,
		fired:        2,
		gateTimeouts: 1,
		passes:       tickPhaseParts{"launch_ms": int64(12), "launch_bd_calls": 4},
		orderCost:    map[string]time.Duration{"slow": 300 * time.Millisecond},
	}}
	cr := ordersLaneTestRuntime(t, od, "1h", nil)
	cr.trace = newSessionReconcilerTraceManager(cr.cityPath, "test-city", io.Discard)

	cr.runOrdersLanePass(context.Background(), cr.cityPath, ordersLaneReasonWake)

	var fields map[string]any
	for _, r := range closeTrace(t, cr) {
		if r.RecordType == TraceRecordOperation && r.SiteCode == TraceSiteOrderDispatch && r.Fields["operation_name"] == "dispatch_orders" {
			fields = r.Fields
		}
	}
	if fields == nil {
		t.Fatal("the lane pass wrote no dispatch_orders operation record")
	}
	for _, key := range []string{
		"rescan_ms", "rescan_bd_calls",
		"install_ms", "install_bd_calls",
		"tracking_sweep_ms", "tracking_sweep_bd_calls",
		"tracking_retention_ms", "tracking_retention_bd_calls",
		"nudge_mail_sweep_ms", "nudge_mail_sweep_bd_calls",
		"dispatch_ms", "dispatch_bd_calls",
		"launch_ms", "launch_bd_calls",
		"bd_calls", "bd_ms",
	} {
		if _, ok := fields[key]; !ok {
			t.Errorf("dispatch_orders record lacks %s: %#v", key, fields)
		}
	}
	for key, want := range map[string]string{
		"orders":         "7",
		"candidates":     "5",
		"due":            "3",
		"fired":          "2",
		"gate_timeouts":  "1",
		"slowest_orders": "slow=300ms",
	} {
		if got := fmt.Sprint(fields[key]); got != want {
			t.Errorf("dispatch_orders %s = %q, want %q", key, got, want)
		}
	}
}
