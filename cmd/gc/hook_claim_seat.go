package main

import (
	"os"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// hookClaimSeat answers one question for the fresh-claim tier: does this
// candidate's recorded checkout belong to ANOTHER seat? (vn-3qa7dmi)
//
// An unassigned, routed bead looks free to the work query, but "unassigned" is
// only the ledger's view. A bead can be released while its work still sits in a
// worker's tree: its lease lapsed while the worker was busy on a molecule step
// bead, or a sweep cleared the assignee of a session that is about to come back.
// The checkout the bead records (gc.work_dir, then the legacy work_dir) is the
// one fact that survives that release. When it names a live checkout that is not
// the claiming session's, a claim hands a second worker the same bead, and
// nothing goes red: the second worker starts fresh or fights the first over the
// branch. Measured twice in one pool, 2026-09-30 and 2026-10-06.
//
// So the tier skips such a candidate and puts a candidate recorded under the
// claiming session's OWN checkout first. The next session of a seat then gets
// back the bead its predecessor left in that tree, instead of a stranger's.
//
// A release that really frees a bead clears both work-dir keys, so the guard
// never holds a bead that was handed back properly. The guard fails open
// everywhere it cannot be sure: no session, a session whose checkout cannot be
// read, a recorded path that is gone, is not a git checkout, or is the store
// itself (a bead stamped from the shared checkout names no seat at all).
type hookClaimSeat struct {
	sessionID string
	resolve   hookResolveSessionWorkDirFunc
	storeDir  string

	ownResolved   bool
	own           string
	storeResolved bool
	store         hookClaimStoreHead
}

func newHookClaimSeat(opts hookClaimOptions, ops hookClaimOps, storeDir string) *hookClaimSeat {
	return &hookClaimSeat{
		sessionID: hookClaimSessionID(opts.Env),
		resolve:   ops.ResolveSessionSeatDir,
		storeDir:  storeDir,
	}
}

// ownDir is the claiming session's checkout, resolved once and only when some
// candidate records a checkout, so a claim over beads that record none costs
// no session read.
func (s *hookClaimSeat) ownDir() string {
	if !s.ownResolved {
		s.ownResolved = true
		if s.sessionID != "" && s.resolve != nil {
			s.own = strings.TrimSpace(s.resolve(s.sessionID))
		}
	}
	return s.own
}

func (s *hookClaimSeat) storeHead() hookClaimStoreHead {
	if !s.storeResolved {
		s.storeResolved = true
		s.store = hookClaimResolveStoreHead(s.storeDir)
	}
	return s.store
}

// isOwn reports whether candidate records the claiming session's own checkout.
func (s *hookClaimSeat) isOwn(candidate beads.Bead) bool {
	recorded := hookClaimRecordedWorkDirs(candidate)
	if len(recorded) == 0 || s.ownDir() == "" {
		return false
	}
	for _, dir := range recorded {
		if hookClaimSameDir(dir, s.own) {
			return true
		}
	}
	return false
}

// heldElsewhere returns the other seat's checkout when candidate records one
// that still exists as a git checkout, is not the store, and is not the
// claiming session's own.
func (s *hookClaimSeat) heldElsewhere(candidate beads.Bead) (string, bool) {
	recorded := hookClaimRecordedWorkDirs(candidate)
	if len(recorded) == 0 || s.ownDir() == "" || s.isOwn(candidate) {
		return "", false
	}
	for _, dir := range recorded {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		// Admit refuses the store under every spelling, and RepoDir is set only
		// when git identified the checkout, so a stale artifact directory or a
		// probe that never answered holds nothing.
		if tree, admitted := s.storeHead().Admit(dir); admitted && tree.RepoDir != "" {
			return dir, true
		}
	}
	return "", false
}

// ownFirst returns candidates with the unassigned ones recorded under the
// claiming session's own checkout moved to the front, keeping the work query's
// order otherwise. Assigned rows are never looked at, so a work query that
// lists other seats' in-progress beads costs no session read here.
func (s *hookClaimSeat) ownFirst(candidates []beads.Bead) []beads.Bead {
	var own, rest []beads.Bead
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Assignee) == "" && s.isOwn(candidate) {
			own = append(own, candidate)
		} else {
			rest = append(rest, candidate)
		}
	}
	if len(own) == 0 {
		return candidates
	}
	return append(own, rest...)
}

// hookClaimRecordedWorkDirs returns the checkouts candidate records, canonical
// key first.
func hookClaimRecordedWorkDirs(candidate beads.Bead) []string {
	var dirs []string
	for _, key := range []string{beadmeta.WorkDirMetadataKey, beadmeta.LegacyWorkDirMetadataKey} {
		if dir := strings.TrimSpace(candidate.Metadata[key]); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}
