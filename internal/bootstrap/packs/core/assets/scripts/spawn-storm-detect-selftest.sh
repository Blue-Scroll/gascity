#!/usr/bin/env bash
# spawn-storm-detect-selftest - prove the detector counts resets, not runs.
#
# Run it with:   spawn-storm-detect.sh --self-test
#
# It never touches the real city. It feeds the detector a made-up event
# stream from a file, sends its mail to a file, and keeps its ledger in a
# temp directory.
#
# The counting rows are the ones bead hq-2yztr2 asked for:
#   1. A bead that sits open and unassigned, never claimed, gets count 0 and
#      no mail, however many times the detector runs.
#   2. A bead claimed and let go 3 times gets count 3, and mail arrives only
#      on the run that saw a new reset.
#   3. A vessel-network bead is counted, not just an hq one.
#   4. A closed bead leaves the ledger.
#
# The progress rows are the ones bead hq-hehd2m asked for:
#   5. Resets with the work moving in between raise NOTHING. This is the
#      town's ordinary refusal-and-fix round, and it used to mail every time.
#   6. Three resets with the work NOT moving raise exactly one mail, and that
#      mail names the count and the sha that never moved.
#
# Then it proves the rows can actually fail. Each fix gets its own mutant,
# because a row only proves something if breaking the thing it guards makes
# it go red:
#   mutant 1 - count every "free" sighting (the hq-2yztr2 bug). The counting
#              rows must fail.
#   mutant 2 - never forgive a reset that came with progress (the hq-hehd2m
#              bug). The progress rows must fail.
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
# One event carrying bead metadata. The three fields are the ones the
# detector builds its progress line from.
ev_meta() { # seq id status assignee title branch rejected_at_sha pr
    printf '{"seq":%s,"type":"bead.updated","ts":"2026-09-20T12:00:00Z","subject":"%s","payload":{"bead":{"id":"%s","status":"%s","assignee":"%s","title":"%s","metadata":{"branch":"%s","rejected_at_sha":"%s","pr_url":"%s"}}}}\n' \
        "$1" "$2" "$2" "$3" "$4" "$5" "$6" "$7" "$8"
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
# The whole sink line for one bead: id, count, subject, body. Row 6 reads it
# to prove the mayor is told the sha that never moved.
mail_line_for() { # bead-id
    grep "^$1	" "$WORK/mail.tsv" 2>/dev/null | head -1
}

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

# ------------------------------------------------- progress fixtures (hq-hehd2m)
# hq-fixloop is the town's ordinary refusal-and-fix round, three times over.
# Each release carries a NEW rejected_at_sha, which means a polecat pushed a
# fix and the refinery judged it again. Real work, moving. This is the exact
# traffic that sent the mayor 30 false alarms in four hours.
#
# hq-deadloop is the real fault: claimed and dropped three times with the same
# branch, the same sha and no PR. Nobody touched the work.
{
    ev_meta 201 hq-fixloop in_progress vessel-network/capable "fix and hand back" b1 "" ""
    ev_meta 202 hq-fixloop open "" "fix and hand back" b1 aaa111 ""
    ev_meta 203 hq-fixloop in_progress vessel-network/nux "fix and hand back" b1 aaa111 ""
    ev_meta 204 hq-fixloop open "" "fix and hand back" b1 bbb222 ""
    ev_meta 205 hq-fixloop in_progress vessel-network/dune "fix and hand back" b1 bbb222 ""
    ev_meta 206 hq-fixloop open "" "fix and hand back" b1 ccc333 ""

    ev_meta 211 hq-deadloop in_progress vessel-network/capable "seat keeps dying" b9 dead00 ""
    ev_meta 212 hq-deadloop open "" "seat keeps dying" b9 dead00 ""
    ev_meta 213 hq-deadloop in_progress vessel-network/nux "seat keeps dying" b9 dead00 ""
    ev_meta 214 hq-deadloop open "" "seat keeps dying" b9 dead00 ""
    ev_meta 215 hq-deadloop in_progress vessel-network/dune "seat keeps dying" b9 dead00 ""
    ev_meta 216 hq-deadloop open "" "seat keeps dying" b9 dead00 ""
} > "$WORK/progress.jsonl"

# ---------------------------------------------------------------- the rows
# Returns 0 when every row passes. Used for the real script AND the mutant.
run_rows() {
    local before=$FAILURES
    reset_world

    run_detector "$WORK/phase1.jsonl"
    [ "$(resets_of hq-quiet)" = "0" ] || fail "row 1: hq-quiet was never claimed, so its count must be 0, got '$(resets_of hq-quiet)'"
    [ "$(mail_count_for hq-quiet)" = "0" ] || fail "row 1: hq-quiet must raise no mail, got $(mail_count_for hq-quiet)"
    [ "$(resets_of hq-storm)" = "2" ] || fail "row 2: hq-storm was let go twice, expected 2, got '$(resets_of hq-storm)'"
    # Two is under the threshold of 3, which is the hq-hehd2m change: one
    # refusal-and-fix round is not a storm.
    [ "$(mail_count_for hq-storm)" = "0" ] || fail "row 2: two resets are under the threshold and must stay quiet, got $(mail_count_for hq-storm)"
    [ "$(resets_of vn-rigbead)" = "2" ] || fail "row 3: the rig bead was let go twice, expected 2, got '$(resets_of vn-rigbead)'"
    [ "$(mail_count_for vn-rigbead)" = "0" ] || fail "row 3: the rig bead is under the threshold and must stay quiet, got $(mail_count_for vn-rigbead)"

    local mail_after_run1
    mail_after_run1="$(mail_lines)"

    # Run 2: no new reset anywhere. This is the reported bug. Nothing may be
    # sent, and no count may move.
    run_detector "$WORK/phase2.jsonl"
    [ "$(mail_lines)" = "$mail_after_run1" ] || fail "row 1: a run with no new reset sent mail ($mail_after_run1 -> $(mail_lines))"
    [ "$(resets_of hq-quiet)" = "0" ] || fail "row 1: hq-quiet's count moved on a run that saw no claim, got '$(resets_of hq-quiet)'"
    [ "$(resets_of hq-storm)" = "2" ] || fail "row 2: hq-storm's count moved with no new bounce, got '$(resets_of hq-storm)'"

    # Run 3: one new real bounce takes it to 3, the threshold. One mail.
    run_detector "$WORK/phase3.jsonl"
    [ "$(resets_of hq-storm)" = "3" ] || fail "row 2: after a third bounce expected 3, got '$(resets_of hq-storm)'"
    [ "$(mail_count_for hq-storm)" = "1" ] || fail "row 2: the third reset reaches the threshold and must send exactly 1 mail, got $(mail_count_for hq-storm)"

    # Run 4: closing the bead takes it out of the ledger.
    run_detector "$WORK/phase4.jsonl"
    [ "$(resets_of hq-storm)" = "absent" ] || fail "row 4: a closed bead must leave the ledger, got '$(resets_of hq-storm)'"

    [ "$FAILURES" -eq "$before" ]
}

# Rows 5 and 6: the hq-hehd2m fix. A reset is only a storm when the work is
# standing still. Returns 0 when both rows pass.
run_progress_rows() {
    local before=$FAILURES line=""
    reset_world

    run_detector "$WORK/progress.jsonl"

    # Row 5. Three releases, and every one carried a new rejected_at_sha, so a
    # polecat pushed and the refinery re-judged each time. The count is set
    # back to zero by each of those moves, so it never reaches the threshold.
    [ "$(resets_of hq-fixloop)" = "1" ] || fail "row 5: work moved between every reset, so the count must fall back to 1, got '$(resets_of hq-fixloop)'"
    [ "$(mail_count_for hq-fixloop)" = "0" ] || fail "row 5: a refusal-and-fix round must raise NO mail, got $(mail_count_for hq-fixloop)"

    # Row 6. Same three releases, nothing moving. One mail, and it must tell
    # the reader the count and the sha that never moved, or the mayor cannot
    # judge it without opening the bead.
    [ "$(resets_of hq-deadloop)" = "3" ] || fail "row 6: three resets with nothing moving expected 3, got '$(resets_of hq-deadloop)'"
    [ "$(mail_count_for hq-deadloop)" = "1" ] || fail "row 6: three stuck resets must raise exactly 1 mail, got $(mail_count_for hq-deadloop)"
    line="$(mail_line_for hq-deadloop)"
    case "$line" in
        *"reset 3x"*) ;;
        *) fail "row 6: the mail must name the count, got: $line" ;;
    esac
    case "$line" in
        *"dead00"*) ;;
        *) fail "row 6: the mail must name the sha that never moved, got: $line" ;;
    esac
    case "$line" in
        *"branch=b9"*) ;;
        *) fail "row 6: the mail must name the branch that never moved, got: $line" ;;
    esac

    [ "$FAILURES" -eq "$before" ]
}

echo "spawn-storm-detect --self-test"
echo "checking the real script:"
PHASE="real script"
if run_rows; then
    ok "counting rows pass"
else
    echo "  the script under test is broken" >&2
fi
if run_progress_rows; then
    ok "progress rows pass"
else
    echo "  the script under test is broken" >&2
fi

# ------------------------------------------------------------- the mutants
# Each fix gets its own mutant. A row only proves something if breaking the
# thing it guards makes it go red, so both are run and both must fail.
#
# The edit is checked two ways before it is trusted: it must match EXACTLY
# one place, and it must really change the file. A find-and-replace that hits
# nothing, or that hits a second line as well, is a check proving nothing
# while printing a pass.
cp "$(dirname "$TARGET")/_bd_trace.sh" "$WORK/_bd_trace.sh"

check_mutant() { # name pattern replacement rows-function
    local name="$1" pattern="$2" replacement="$3" rows="$4"
    local mutant="$WORK/mutant.sh" hits=0 before=0
    echo "checking the rows can fail ($name):"
    # -F: the anchors are jq source full of $ ( ) " characters. Matching them
    # as a regex happens to work today and would quietly stop working the day
    # an anchor gains a . or a *, which reads as a passing check.
    hits=$(grep -c -F -- "$pattern" "$TARGET" || true)
    if [ "$hits" != "1" ]; then
        echo "  FAIL: the mutant anchor for '$name' matched $hits lines in spawn-storm-detect.sh, want exactly 1. The anchor moved, so this check was proving nothing." >&2
        FAILURES=$((FAILURES + 1))
        return
    fi
    awk -v pat="$pattern" -v rep="$replacement" '
        { i = index($0, pat)
          if (i > 0) { $0 = substr($0, 1, i - 1) rep substr($0, i + length(pat)) }
          print }' "$TARGET" > "$mutant"
    if cmp -s "$TARGET" "$mutant"; then
        echo "  FAIL: the mutant edit for '$name' changed nothing." >&2
        FAILURES=$((FAILURES + 1))
        return
    fi
    CURRENT_SCRIPT="$mutant"
    PHASE="$name, expected to fail"
    before=$FAILURES
    if "$rows"; then
        echo "  FAIL: '$name' passed every row. The rows do not detect that bug." >&2
        FAILURES=$((before + 1))
    else
        # The mutant's failures are the point, not a problem. Roll them back.
        FAILURES=$before
        ok "$name fails its rows, as it must"
    fi
    CURRENT_SCRIPT="$TARGET"
    PHASE="real script"
}

# Mutant 1 (hq-2yztr2): count every sighting of a free bead instead of every
# move into free.
check_mutant "mutant 1: count every sighting" \
    '($prev == "held" and $next == "free")' '($next == "free")' run_rows

# Mutant 2 (hq-hehd2m): never forgive a reset that came with progress, which
# is what made every refusal-and-fix round look like a crash loop.
check_mutant "mutant 2: never forgive progress" \
    '($prev_progress != "" and $progress != $prev_progress)' '(false)' run_progress_rows

if [ "$FAILURES" -eq 0 ]; then
    echo "spawn-storm-detect: self-test PASSED"
    exit 0
fi
echo "spawn-storm-detect: self-test FAILED ($FAILURES problems)" >&2
exit 1
