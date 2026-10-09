//go:build gascity_native_beads

package beads

import "testing"

// A HasMetadataKey read is cut Go-side in doltlite (vn-d5jn83b), so it must not
// take the bounded SQL top-N path, which cuts before that filter runs.
func TestDoltliteBoundedTopNRefusesHasMetadataKey(t *testing.T) {
	sets := []doltliteTableSet{{}, {}}
	if !doltliteCanSelectBoundedTopN(ListQuery{Limit: 5}, sets, "", 5, "") {
		t.Fatal("control: a plain bounded two-table read should use the bounded path")
	}
	if doltliteCanSelectBoundedTopN(ListQuery{Limit: 5, HasMetadataKey: "k"}, sets, "", 5, "") {
		t.Fatal("a HasMetadataKey read is cut Go-side and must not take the bounded SQL path")
	}
}
