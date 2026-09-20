#!/usr/bin/env bash
# spawn-storm-detect-selftest - prove the detector counts resets, not runs.
#
# Run it with:   spawn-storm-detect.sh --self-test
#
# It never touches the real city. It feeds the detector a made-up event
# stream from a file, sends its mail to a file, and keeps its ledger in a
# temp directory.
#
# The rows it proves are the ones bead hq-2yztr2 asked for:
#   1. A bead that sits open and unassigned, never claimed, gets count 0 and
#      no mail, however many times the detector runs.
#   2. A bead claimed and let go 3 times gets count 3, and mail arrives only
#      on the run that saw a new reset.
#   3. A vessel-network bead is counted, not just an hq one.
#   4. A closed bead leaves the ledger.
#
# Then it proves the rows can actually fail: it makes a copy of the detector
# with the reset test broken back to the old "count every sighting" bug, and
# requires rows 1 and 2 to fail against that copy.
set -uo pipefail

TARGET="${1:-}"
if [ -z "$TARGET" ] || [ ! -f "$TARGET" ]; then
    echo "usage: spawn-storm-detect.sh --self-test" >&2
    exit 2
fi
TARGET="$(cd "$(dirname "$TARGET")" && pwd)/$(basename "$TARGET")"

command -v jq >/dev/null 2>&1 || { echo "self-test needs jq" >&2; exit 2; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

FAILURES=0
CURRENT_SCRIPT="$TARGET"
# What is being checked right now. It prefixes every message, so the mutant's
# failures cannot be mistaken for the real script's.
PHASE="real script"

fail() {
    echo "  [$PHASE] FAIL: $*" >&2
    FAILURES=$((FAILURES + 1))
}

ok() { echo "  ok: $*"; }

# One event line. The shape matches `gc events` output.
ev() { # seq id status assignee title
    printf '{"seq":%s,"type":"bead.updated","ts":"2026-09-20T12:00:00Z","subject":"%s","payload":{"bead":{"id":"%s","status":"%s","assignee":"%s","title":"%s"}}}\n' \
        "$1" "$2" "$2" "$3" "$4" "$5"
}
ev_closed() { # seq id
    printf '{"seq":%s,"type":"bead.closed","ts":"2026-09-20T12:00:00Z","subject":"%s","payload":{"bead":{"id":"%s","status":"closed","assignee":"","title":"gone"}}}\n' \
        "$1" "$2" "$2"
}

# Run the detector under test over one events file.
run_detector() { # events-file
    SPAWN_STORM_EVENTS_FILE="$1" \
    SPAWN_STORM_MAIL_SINK="$WORK/mail.tsv" \
    GC_PACK_STATE_DIR="$WORK/state" \
    bash "$CURRENT_SCRIPT" >/dev/null 2>"$WORK/stderr.log"
}

reset_world() {
    rm -rf "$WORK/state"
    : > "$WORK/mail.tsv"
}

resets_of() { # bead-id
    jq -r --arg id "$1" '.beads[$id].resets // "absent"' "$WORK/state/spawn-storm-counts.json" 2>/dev/null || echo "absent"
}
mail_count_for() { # bead-id
    grep -c "^$1	" "$WORK/mail.tsv" 2>/dev/null || true
}
mail_lines() { wc -l < "$WORK/mail.tsv" | tr -d ' '; }

# ---------------------------------------------------------------- fixtures
# A quiet bead: seen open and unassigned over and over, never claimed. This is
# the exact shape that made the old detector mail "reset 2x, 3x, 4x".
{
    ev 101 hq-quiet open "" "waiting in a pool line"
    ev 102 hq-quiet open "" "waiting in a pool line"
    ev 103 hq-quiet open "" "waiting in a pool line"
    # A bead really bouncing: claimed and let go twice.
    ev 104 hq-storm in_progress vessel-network/capable "crash looping work"
    ev 105 hq-storm open "" "crash looping work"
    ev 106 hq-storm in_progress vessel-network/nux "crash looping work"
    ev 107 hq-storm open "" "crash looping work"
    # A rig bead. The old detector could not see one of these at all.
    ev 108 vn-rigbead in_progress vessel-network/dune "rig side work"
    ev 109 vn-rigbead open "" "rig side work"
    ev 110 vn-rigbead in_progress vessel-network/shard "rig side work"
    ev 111 vn-rigbead open "" "rig side work"
} > "$WORK/phase1.jsonl"

# Nothing new for the bouncing beads. Only more sightings of the quiet one,
# with fresh sequence numbers, exactly like a bead waiting in a pool line.
{
    cat "$WORK/phase1.jsonl"
    ev 120 hq-quiet open "" "waiting in a pool line"
    ev 121 hq-quiet open "" "waiting in a pool line"
    ev 122 hq-quiet open "" "waiting in a pool line"
} > "$WORK/phase2.jsonl"

# One more real bounce for hq-storm, taking it to 3.
{
    cat "$WORK/phase2.jsonl"
    ev 130 hq-storm in_progress vessel-network/rictus "crash looping work"
    ev 131 hq-storm open "" "crash looping work"
} > "$WORK/phase3.jsonl"

{
    cat "$WORK/phase3.jsonl"
    ev_closed 140 hq-storm
} > "$WORK/phase4.jsonl"

# ---------------------------------------------------------------- the rows
# Returns 0 when every row passes. Used for the real script AND the mutant.
run_rows() {
    local before=$FAILURES
    reset_world

    run_detector "$WORK/phase1.jsonl"
    [ "$(resets_of hq-quiet)" = "0" ] || fail "row 1: hq-quiet was never claimed, so its count must be 0, got '$(resets_of hq-quiet)'"
    [ "$(mail_count_for hq-quiet)" = "0" ] || fail "row 1: hq-quiet must raise no mail, got $(mail_count_for hq-quiet)"
    [ "$(resets_of hq-storm)" = "2" ] || fail "row 2: hq-storm was let go twice, expected 2, got '$(resets_of hq-storm)'"
    [ "$(mail_count_for hq-storm)" = "1" ] || fail "row 2: hq-storm must raise 1 mail at the threshold, got $(mail_count_for hq-storm)"
    [ "$(resets_of vn-rigbead)" = "2" ] || fail "row 3: the rig bead was let go twice, expected 2, got '$(resets_of vn-rigbead)'"
    [ "$(mail_count_for vn-rigbead)" = "1" ] || fail "row 3: the rig bead must raise 1 mail, got $(mail_count_for vn-rigbead)"

    local mail_after_run1
    mail_after_run1="$(mail_lines)"

    # Run 2: no new reset anywhere. This is the reported bug. Nothing may be
    # sent, and no count may move.
    run_detector "$WORK/phase2.jsonl"
    [ "$(mail_lines)" = "$mail_after_run1" ] || fail "row 1: a run with no new reset sent mail ($mail_after_run1 -> $(mail_lines))"
    [ "$(resets_of hq-quiet)" = "0" ] || fail "row 1: hq-quiet's count moved on a run that saw no claim, got '$(resets_of hq-quiet)'"
    [ "$(resets_of hq-storm)" = "2" ] || fail "row 2: hq-storm's count moved with no new bounce, got '$(resets_of hq-storm)'"

    # Run 3: one new real bounce. Count 3, and exactly one new mail.
    run_detector "$WORK/phase3.jsonl"
    [ "$(resets_of hq-storm)" = "3" ] || fail "row 2: after a third bounce expected 3, got '$(resets_of hq-storm)'"
    [ "$(mail_count_for hq-storm)" = "2" ] || fail "row 2: a new reset past the threshold must send one more mail, got $(mail_count_for hq-storm)"

    # Run 4: closing the bead takes it out of the ledger.
    run_detector "$WORK/phase4.jsonl"
    [ "$(resets_of hq-storm)" = "absent" ] || fail "row 4: a closed bead must leave the ledger, got '$(resets_of hq-storm)'"

    [ "$FAILURES" -eq "$before" ]
}

echo "spawn-storm-detect --self-test"
echo "checking the real script:"
PHASE="real script"
if run_rows; then
    ok "all rows pass"
else
    echo "  the script under test is broken" >&2
fi

# ------------------------------------------------------------- the mutant
# Put the old bug back: count every sighting of a free bead instead of every
# move into free. If the rows still pass against that, the rows prove nothing.
echo "checking the rows can fail (mutant: count every sighting, the old bug):"
MUTANT="$WORK/mutant.sh"
sed 's/(\$prev == "held" and \$next == "free")/($next == "free")/' "$TARGET" > "$MUTANT"
if cmp -s "$TARGET" "$MUTANT"; then
    echo "  FAIL: the mutant edit matched nothing. The MUTANT ANCHOR line in spawn-storm-detect.sh moved, so this check was proving nothing." >&2
    FAILURES=$((FAILURES + 1))
else
    # The mutant needs the helper next to it, same as the real script.
    cp "$(dirname "$TARGET")/_bd_trace.sh" "$WORK/_bd_trace.sh"
    CURRENT_SCRIPT="$MUTANT"
    PHASE="mutant, expected to fail"
    MUTANT_FAILURES_BEFORE=$FAILURES
    if run_rows; then
        echo "  FAIL: the mutant passed every row. The rows do not detect the old bug." >&2
        FAILURES=$((MUTANT_FAILURES_BEFORE + 1))
    else
        # The mutant's failures are the point, not a problem. Roll them back.
        FAILURES=$MUTANT_FAILURES_BEFORE
        ok "the mutant fails the rows, as it must"
    fi
    CURRENT_SCRIPT="$TARGET"
    PHASE="real script"
fi

if [ "$FAILURES" -eq 0 ]; then
    echo "spawn-storm-detect: self-test PASSED"
    exit 0
fi
echo "spawn-storm-detect: self-test FAILED ($FAILURES problems)" >&2
exit 1
