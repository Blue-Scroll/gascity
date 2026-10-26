package beadmeta

import "strings"

// HoldMayorLabel and HoldExternalLabel are the two canonical hold:<value>
// bd label values (engdocs/contributors/hold-label-conventions.md,
// ga-tug8ry.1): "the required next actor is the mayor" and "the required
// next actor or condition is outside this bd instance's control",
// respectively. They are bd label *values* (data a bead carries in its
// Labels []string), not role names — a role-neutral dispatcher checks for
// their presence without knowing or caring who "mayor" is (ga-5736js).
const (
	HoldMayorLabel    = "hold:mayor"
	HoldExternalLabel = "hold:external"
)

// HoldLabelPrefix opens every hold:<value> label. The two canonical values
// above are not the whole family: a city mints its own (a squad member is
// parked on hold:squad, a throwaway test bead on hold:scratch), and each one
// means the same thing to a dispatcher — "not yours, do not serve it".
const HoldLabelPrefix = "hold:"

// HoldSquadLabel parks a squad member: a child of its squad bead, built only
// by that squad, never claimed by any seat. The captain claims the SQUAD
// bead, which carries the plain label "squad", not this one. A member that
// still carries a stray gc.routed_to is exactly the row a pool must never be
// handed (vn-c4jeecr), so it gets a bd-side exclude like the canonical two.
const HoldSquadLabel = "hold:squad"

// DispatchHoldLabels is the set of hold label values the generated bd queries
// name in their --exclude-label flags. bd matches a label exactly, so a flag
// can only name values; it cannot name the family. Every Go and jq filter
// matches the whole family instead (IsDispatchHoldLabel), and the hook re-applies
// that filter to whatever bd returns, so a hold:<value> missing from this list
// is still never served. This list keeps the row from being read at all, which
// matters because the routed tier reads a bounded window (--limit=20): a held
// row bd returns takes a slot an unheld row needed. So a hold value that rides
// on routed beads in practice belongs here; hold:squad does (vn-c4jeecr).
//
// Two different questions consume the hold family, and they answer oppositely
// — conflating them is what produced both ga-5736js and gas-kg6:
//
//   - "Is this bead WORK for whoever is asking?" — always no. Route-scoped,
//     unassigned automatic dispatch (Tier 3 pool-demand queries and the
//     control dispatcher's routed/run-target tiers) must exclude these
//     (ga-5736js), and so must every path that serves a bead to an agent as
//     work — including the assignee-scoped crash-recovery tier and the
//     `gc hook --claim` result (gas-kg6). A held bead handed back as work
//     cannot be advanced, is never released, and so is re-served forever.
//
//   - "Does a session still need to EXIST for this bead?" — hold is
//     irrelevant; the assignment is a real ownership fact either way. The
//     demand/liveness tiers that answer this (filterReadyByAssignee,
//     ephemeralAssignedReadyProbeScript) stay hold-transparent by design and
//     must never filter on holds (ga-5736js), or a parked bead's owner
//     would go invisible to the pool and to crash recovery.
//
// The short rule: filter on holds when deciding what to DO, never when
// deciding who EXISTS.
var DispatchHoldLabels = []string{HoldMayorLabel, HoldExternalLabel, HoldSquadLabel}

// IsDispatchHoldLabel reports whether label is any hold:<value> label, compared
// case-insensitively after trimming. It is the one test for "this bead is
// parked for someone else". Matching only the canonical values let a stray
// route on a hold:squad member spawn a pool seat that found nothing to claim,
// once per re-route (vn-ar56o6i). Labels that merely look hold-ish
// (`mpr-human-hold`, the routing label `needs-mayor`) do not start with the
// prefix, so they stay ordinary work.
func IsDispatchHoldLabel(label string) bool {
	label = strings.TrimSpace(label)
	return len(label) >= len(HoldLabelPrefix) && strings.EqualFold(label[:len(HoldLabelPrefix)], HoldLabelPrefix)
}

// HasDispatchHoldLabel reports whether any of labels is a hold:<value> label.
func HasDispatchHoldLabel(labels []string) bool {
	for _, label := range labels {
		if IsDispatchHoldLabel(label) {
			return true
		}
	}
	return false
}
