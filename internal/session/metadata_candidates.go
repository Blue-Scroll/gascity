package session

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

// ExactMetadataSessionCandidates returns session beads matching any exact
// metadata filter. Each filter must contain exactly one key/value pair; empty
// filters are ignored. Results are deduplicated by bead ID in query order.
func ExactMetadataSessionCandidates(store beads.Store, includeClosed bool, filters ...map[string]string) ([]beads.Bead, error) {
	return exactMetadataSessionCandidates(store, includeClosed, "", filters...)
}

// ExactMetadataSessionCandidatesWithStatus returns session beads matching any
// exact metadata filter and the requested bead status.
func ExactMetadataSessionCandidatesWithStatus(store beads.Store, status string, filters ...map[string]string) ([]beads.Bead, error) {
	return exactMetadataSessionCandidates(store, false, strings.TrimSpace(status), filters...)
}

// ExactMetadataSessionCandidatesInfo is the session.Info-projecting sibling of
// ExactMetadataSessionCandidates: it returns the projected session.Info of each
// candidate bead, applying the codec once here at the store edge so no raw bead
// escapes. It shares the dedup / query order / IsSessionBeadOrRepairable
// semantics of ExactMetadataSessionCandidates. It is the typed feed for the
// named-session retire lane, which needs only Info fields per candidate.
func ExactMetadataSessionCandidatesInfo(store beads.Store, includeClosed bool, filters ...map[string]string) ([]Info, error) {
	candidates, err := exactMetadataSessionCandidates(store, includeClosed, "", filters...)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(candidates))
	for _, b := range candidates {
		out = append(out, infoFromPersistedBead(b))
	}
	return out, nil
}

// sessionNameClaimCandidates returns the session beads that can bear on
// whether a session may claim name: every bead whose session_name is name,
// closed ones included (an explicit session_name stays reserved after its bead
// closes), plus every OPEN bead whose alias, agent_name, template or
// common_name is name. A closed bead matched only by one of those four never
// reserves the name, so they are not read for closed rows.
//
// Keep it that way. A lookup that reads closed rows cannot be answered from
// the CachingStore's open rows, so it is a JSON scan of the whole ledger:
// about 3 s each on hq (13,851 rows) at load 150. The name check used to run
// five of them per name, and that was most of a pool session create's 28 s to
// 45 s inside the demand tick (vn-8tupvb1). The four open-only lookups are
// Live, so a bead another process opened after the cache last refreshed still
// counts: the same freshness the closed-inclusive reads had, since those
// always reached the backing store.
func sessionNameClaimCandidates(store beads.Store, name string) ([]beads.Bead, error) {
	set := newSessionCandidateSet(store)
	if err := set.add(beads.ListQuery{IncludeClosed: true}, map[string]string{"session_name": name}); err != nil {
		return nil, err
	}
	if err := set.add(beads.ListQuery{Live: true},
		map[string]string{"alias": name},
		map[string]string{"agent_name": name},
		map[string]string{"template": name},
		map[string]string{"common_name": name},
	); err != nil {
		return nil, err
	}
	return set.candidates, nil
}

// liveOpenSessionNameCandidates is sessionNameClaimCandidates for a check that
// only open beads can fail: every field, session_name too, is read open-only
// and Live.
func liveOpenSessionNameCandidates(store beads.Store, name string) ([]beads.Bead, error) {
	set := newSessionCandidateSet(store)
	if err := set.add(beads.ListQuery{Live: true},
		map[string]string{"session_name": name},
		map[string]string{"alias": name},
		map[string]string{"agent_name": name},
		map[string]string{"template": name},
		map[string]string{"common_name": name},
	); err != nil {
		return nil, err
	}
	return set.candidates, nil
}

func exactMetadataSessionCandidates(store beads.Store, includeClosed bool, status string, filters ...map[string]string) ([]beads.Bead, error) {
	base := beads.ListQuery{IncludeClosed: includeClosed}
	if status != "" {
		base = beads.ListQuery{Status: status}
	}
	set := newSessionCandidateSet(store)
	if err := set.add(base, filters...); err != nil {
		return nil, err
	}
	return set.candidates, nil
}

// sessionCandidateSet collects session beads from one or more exact metadata
// lookups, deduplicated by bead ID in query order.
type sessionCandidateSet struct {
	store      beads.Store
	seenBeads  map[string]bool
	candidates []beads.Bead
}

func newSessionCandidateSet(store beads.Store) *sessionCandidateSet {
	return &sessionCandidateSet{store: store, seenBeads: make(map[string]bool)}
}

// add runs one store.List per filter, each being base plus that filter's one
// metadata key/value pair. Empty and repeated filters are skipped.
func (s *sessionCandidateSet) add(base beads.ListQuery, filters ...map[string]string) error {
	if s.store == nil {
		return nil
	}
	seenQueries := make(map[string]bool, len(filters))
	for _, filter := range filters {
		if len(filter) != 1 {
			continue
		}
		var key, value string
		for k, v := range filter {
			key = strings.TrimSpace(k)
			value = strings.TrimSpace(v)
		}
		if key == "" || value == "" {
			continue
		}
		queryKey := key + "\x00" + value
		if seenQueries[queryKey] {
			continue
		}
		seenQueries[queryKey] = true
		query := base
		query.Metadata = map[string]string{key: value}
		items, err := s.store.List(query)
		if err != nil {
			return err
		}
		for _, b := range items {
			if s.seenBeads[b.ID] || !IsSessionBeadOrRepairable(b) {
				continue
			}
			RepairEmptyType(s.store, &b)
			s.seenBeads[b.ID] = true
			s.candidates = append(s.candidates, b)
		}
	}
	return nil
}
