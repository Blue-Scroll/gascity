package beadmeta

import "testing"

// TestDispatchHoldLabelsMatchCanonicalHoldValues pins beadmeta as the single
// named home for the two canonical hold values documented in
// engdocs/contributors/hold-label-conventions.md (hold:mayor, hold:external)
// so internal/config and cmd/gc can share one definition instead of each
// re-spelling the label strings (ga-x9kptu / ga-5736js). It also pins
// hold:squad on the bd-side exclude list, so a squad member with a stray
// route never fills a slot of the routed tier's window (vn-c4jeecr).
func TestDispatchHoldLabelsMatchCanonicalHoldValues(t *testing.T) {
	if HoldMayorLabel != "hold:mayor" {
		t.Fatalf("HoldMayorLabel = %q, want %q", HoldMayorLabel, "hold:mayor")
	}
	if HoldExternalLabel != "hold:external" {
		t.Fatalf("HoldExternalLabel = %q, want %q", HoldExternalLabel, "hold:external")
	}
	if HoldSquadLabel != "hold:squad" {
		t.Fatalf("HoldSquadLabel = %q, want %q", HoldSquadLabel, "hold:squad")
	}
	want := []string{HoldMayorLabel, HoldExternalLabel, HoldSquadLabel}
	if len(DispatchHoldLabels) != len(want) {
		t.Fatalf("DispatchHoldLabels = %#v, want %#v", DispatchHoldLabels, want)
	}
	for i, v := range want {
		if DispatchHoldLabels[i] != v {
			t.Fatalf("DispatchHoldLabels[%d] = %q, want %q", i, DispatchHoldLabels[i], v)
		}
	}
}

// TestIsDispatchHoldLabelMatchesTheWholeHoldFamily pins the family rule: any
// hold:<value> is a hold, whatever the value, so a city-minted hold such as
// hold:squad is never served or counted as pool demand (vn-ar56o6i).
func TestIsDispatchHoldLabelMatchesTheWholeHoldFamily(t *testing.T) {
	for _, label := range []string{"hold:mayor", "hold:external", "hold:squad", "hold:scratch", "Hold:Squad", " hold:squad "} {
		if !IsDispatchHoldLabel(label) {
			t.Errorf("IsDispatchHoldLabel(%q) = false, want true", label)
		}
	}
	for _, label := range []string{"", "hold", "mpr-human-hold", "needs-mayor", "squad", "on-hold", "threshold:hold:x"} {
		if IsDispatchHoldLabel(label) {
			t.Errorf("IsDispatchHoldLabel(%q) = true, want false", label)
		}
	}
	for _, label := range DispatchHoldLabels {
		if !IsDispatchHoldLabel(label) {
			t.Errorf("canonical DispatchHoldLabels value %q is not in the hold family", label)
		}
	}
	if !HasDispatchHoldLabel([]string{"town-tooling", "hold:squad"}) {
		t.Error("HasDispatchHoldLabel missed hold:squad among other labels")
	}
	if HasDispatchHoldLabel([]string{"town-tooling", "squad"}) || HasDispatchHoldLabel(nil) {
		t.Error("HasDispatchHoldLabel matched a bead with no hold label")
	}
}
