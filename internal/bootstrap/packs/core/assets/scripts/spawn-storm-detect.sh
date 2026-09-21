#!/usr/bin/env bash
# spawn-storm-detect - find beads stuck in a claim-and-release loop.
#
# A "spawn storm" is one bead that keeps being claimed by a seat and then
# pushed back to the pool. That is a crash loop on that piece of work, and
# the mayor should look at it.
#
# HOW IT COUNTS
# It reads the city event stream (gc events) and walks each bead's state
# changes in order. A reset is one move from "held" to "free":
#
#   held   = someone has it. Status is in_progress, or there is an assignee.
#   free   = it is back in the pool. Status is open AND the assignee is empty.
#   closed = done. The bead is dropped from the ledger.
#
# So the count is "how many times this bead went back to the pool". It is
# not "how many times this script ran".
#
# A RESET IS ONLY A STORM WHEN THE WORK IS NOT MOVING (hq-hehd2m)
# Counting real resets was still not enough. The town's normal fix-and-hand-
# back loop IS a reset: a pr-review refusal moves a bead from claimed to
# open-and-unassigned every time, and that is the loop working, not a crash.
# So before counting, the detector asks whether the bead's work moved. It
# builds one line from the three fields that change when it does:
#
#   branch=<metadata.branch>  sha=<metadata.rejected_at_sha>  pr=<its PR>
#
# If that line changed, somebody pushed, got re-judged, or opened a PR. The
# count goes back to zero and nothing is said. The count only climbs while
# the line stays EXACTLY the same, which is the shape of a seat that claims
# the work and dies without touching it.
#
# THE HOLE THIS LEAVES, SAID OUT LOUD
# The sha and the PR are strong evidence that code moved. The branch NAME is
# weaker: a seat that claims a bead with no branch yet, writes its own
# branch name at branch-setup and then dies would change the line every
# round and never be reported. Measured over 8 hours of real traffic, 11 of
# the 13 beads forgiven here kept ONE branch name and were forgiven by the
# sha, so this is not what is happening today. It is still a way for a real
# crash loop to hide, and vn-tweh24y is open to close it.
#
# Measured 2026-09-21: 30 SPAWN_STORM mails reached the mayor in about four
# hours. Every one was triaged. ZERO were a crash loop. An alarm that is
# wrong 30 times out of 30 teaches everyone to ignore it, so the real crash
# loop it exists for arrives in words nobody reads any more.
#
# WHAT THIS REPLACED, AND WHY (hq-2yztr2)
# The old version listed every open unassigned bead, kept the ones carrying
# rejection_reason or recovered metadata, and added 1 to each on EVERY run.
# A bead that simply waited in a pool line therefore climbed one count per
# run and mailed the mayor every run. On 2026-09-20 bead hq-uvlj6b was
# claimed zero times in 29 minutes and still collected SPAWN_STORM mails
# saying "reset 2x, 3x, 4x", and later "13x". The mayor acted on one of
# those mails and quarantined the bead for a cause that was not real.
#
# It was blind the other way too. Earlier the SAME bead was claimed and let
# go 4 times in 43 minutes and the detector said nothing, because those
# releases wrote no rejection metadata. Counting state changes fixes both
# directions, and the event stream carries rig beads (vn-*) as well as hq
# beads, which the old `gc bd list` call could not see because it passed no
# --rig.
#
# Runs as an exec order (no LLM, no agent, no wisp).
set -euo pipefail

# Trace bd invocations to $GC_BD_TRACE when set (no-op otherwise).
__SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
. "$__SCRIPT_DIR/_bd_trace.sh" "spawn-storm-detect"

if [ "${1:-}" = "--self-test" ]; then
    # `bash <file>` on purpose, not exec: the pack installer does not have to
    # keep an exec bit for the self-test to run.
    exec bash "$__SCRIPT_DIR/spawn-storm-detect-selftest.sh" "${BASH_SOURCE[0]}"
fi
if [ "$#" -gt 0 ]; then
    echo "spawn-storm-detect: unknown argument '$1' (only --self-test is accepted)" >&2
    exit 2
fi

CITY="${GC_CITY:-.}"
PACK_STATE_DIR="${GC_PACK_STATE_DIR:-${GC_CITY_RUNTIME_DIR:-$CITY/.gc/runtime}/packs/core}"
LEDGER="$PACK_STATE_DIR/spawn-storm-counts.json"
# Three, not two. Two is one ordinary refusal-and-fix round, which every
# healthy bead has. Three claims with nothing moving in between is a seat
# that is not doing the work (hq-hehd2m).
THRESHOLD="${SPAWN_STORM_THRESHOLD:-3}"

# How far back to ask the event stream. This MUST be comfortably longer than
# the order's interval (5m, see spawn-storm-detect.toml), because a reset that
# falls outside the window is never seen. Overlap is free: every event carries
# a sequence number and the ledger remembers the highest one it counted, so
# reading the same event twice counts it once.
WINDOW="${SPAWN_STORM_WINDOW:-1h}"

# Forget a bead the detector has not seen move for this many runs. A storm is
# about a bead that is moving, and this is what keeps the ledger small. At the
# 5m order interval, 288 runs is about a day.
RETENTION_RUNS="${SPAWN_STORM_RETENTION_RUNS:-288}"

# Two seams, both for --self-test. Neither one runs a command.
#   SPAWN_STORM_EVENTS_FILE - read events from this JSONL file instead of
#     calling `gc events`. It also accepts a raw .gc/events.jsonl, whose
#     payload holds the bead directly instead of under a "bead" key.
#   SPAWN_STORM_MAIL_SINK - append "<bead-id><tab><count>" to this file
#     instead of mailing the mayor.
EVENTS_FILE="${SPAWN_STORM_EVENTS_FILE:-}"
MAIL_SINK="${SPAWN_STORM_MAIL_SINK:-}"
if [ -n "$EVENTS_FILE" ] || [ -n "$MAIL_SINK" ]; then
    echo "spawn-storm-detect: TEST SEAMS ARE SET. events_file='$EVENTS_FILE' mail_sink='$MAIL_SINK'" >&2
fi

if [ ! -e "$LEDGER" ] && [ -e "$CITY/.gc/spawn-storm-counts.json" ]; then
    LEDGER="$CITY/.gc/spawn-storm-counts.json"
fi
mkdir -p "$(dirname "$LEDGER")"

# Step 1: load the ledger.
#
# Shape:
#   { "version": 3,
#     "runs": <how many times this script has completed>,
#     "cursor_seq": <highest event seq already counted>,
#     "beads": { "<id>": { "state": "held" | "free",
#                          "resets": <returns to the pool with NOTHING moving>,
#                          "mailed": <resets already reported>,
#                          "progress": "<the branch/sha/pr line, see below>",
#                          "seen_run": <run number of its last event>,
#                          "seen": "<timestamp, for humans only>",
#                          "title": "<string>" } } }
#
# Only a version 3 ledger is loaded. Older ones are thrown away, because
# their "resets" was counted by a different rule and the number would be
# read as meaning something it never meant:
#   version 1 - a flat {"<id>": <int>} map of the old RUN counts (hq-2yztr2).
#   version 2 - every real reset, including the refusal-and-fix rounds this
#               version deliberately forgives (hq-hehd2m).
# Dropping them costs one quiet window and buys a number that is true.
LEDGER_JSON='{"version":3,"runs":0,"cursor_seq":0,"beads":{}}'
if [ -f "$LEDGER" ]; then
    LOADED=$(jq -c 'if type == "object" and (.version? == 3) then . else null end' "$LEDGER" 2>/dev/null || echo null)
    if [ -n "$LOADED" ] && [ "$LOADED" != "null" ]; then
        LEDGER_JSON="$LOADED"
    else
        echo "spawn-storm-detect: ledger reset (counted by an older rule, see hq-2yztr2 and hq-hehd2m)" >&2
    fi
fi

# Step 2: read events since the cursor.
#
# A fresh ledger (cursor 0) seeds each bead's state without counting: a reset
# needs a "held" reading BEFORE the "free" one, and the first reading a bead
# gets is its seed. So a first run never mails. That is on purpose.
if [ -n "$EVENTS_FILE" ]; then
    [ -f "$EVENTS_FILE" ] || { echo "spawn-storm-detect: events file not found: $EVENTS_FILE" >&2; exit 2; }
    EVENTS=$(cat "$EVENTS_FILE")
else
    EVENTS=$(gc events --since "$WINDOW") || {
        # Say it out loud. A detector that quietly stops detecting is the same
        # kind of fault it exists to catch.
        echo "spawn-storm-detect: could not read the event stream (gc events --since $WINDOW). Nothing counted this run." >&2
        exit 0
    }
fi
[ -n "$EVENTS" ] || exit 0

# Step 3: walk the events in sequence order and count resets that came with
# no progress. A reset whose progress line moved sets the count back to zero.
NEXT_LEDGER=$(printf '%s\n' "$EVENTS" | jq -c -n \
    --argjson st "$LEDGER_JSON" \
    --argjson retain "$RETENTION_RUNS" '
    # "unknown" is deliberate: an event that carries no status tells us nothing,
    # and guessing "held" there would invent a reset on the next open reading.
    def bead_state($b):
        ($b.status // "") as $s
        | ($b.assignee // "") as $a
        | if $s == "closed" then "closed"
          elif $s == "open" and $a == "" then "free"
          elif $s == "" then "unknown"
          else "held" end;

    # One line naming everything that moves when the work moves. It is BOTH
    # the thing compared and the thing the mail prints, on purpose: a
    # separate message string could say the sha did not move while the
    # comparison was looking at something else.
    #
    # "" means this event carried no metadata object at all, which is not the
    # same as a bead with empty metadata. An absent object tells us nothing,
    # so the caller keeps the line it already had instead of reading the
    # absence as a change.
    def progress_line($b):
        ($b.metadata? // null) as $m
        | if ($m | type) != "object" then ""
          else "branch=" + (($m.branch // "none") | tostring)
             + " sha=" + (($m.rejected_at_sha // "none") | tostring)
             + " pr=" + (($m.pr_url // $m.existing_pr // "none") | tostring)
          end;

    ($st | .runs = ((.runs // 0) + 1)) as $base
    | reduce (inputs
            | select(type == "object")
            | select(.type == "bead.updated" or .type == "bead.created" or .type == "bead.closed")
            | { id: (.subject // ""), seq: (.seq // 0), ts: (.ts // ""),
                bead: (.payload.bead // .payload // {}) }
            | select(.id != "")
           ) as $e
        ($base;
            ($e.id) as $id
            | (.beads[$id].state // "unknown") as $prev
            | (bead_state($e.bead)) as $next
            | ($e.bead.title // "") as $title
            | (.beads[$id].progress // "") as $prev_progress
            | (progress_line($e.bead)) as $line
            # An event with no metadata object keeps the line we already had.
            | (if $line == "" then $prev_progress else $line end) as $progress
            | if $e.seq <= .cursor_seq or $next == "unknown" then .
              else
                .cursor_seq = $e.seq
                | if $next == "closed" then del(.beads[$id])
                  else
                    # MUTANT ANCHOR 2 (do not reflow): forgiving a reset that
                    # came with progress is the hq-hehd2m fix. --self-test
                    # rewrites this one condition to (false) and requires the
                    # progress rows to fail.
                    (if ($prev_progress != "" and $progress != $prev_progress)
                       then .beads[$id].resets = 0 | .beads[$id].mailed = 0
                       else . end)
                    | .beads[$id].resets = ((.beads[$id].resets // 0)
                        # MUTANT ANCHOR (do not reflow): the whole hq-2yztr2 fix
                        # is this one test. Counting a "free" sighting instead of
                        # a move INTO free is the old bug. --self-test rewrites
                        # this line to that broken form and requires the rows to
                        # fail.
                        + (if ($prev == "held" and $next == "free") then 1 else 0 end))
                    | .beads[$id].mailed = (.beads[$id].mailed // 0)
                    | .beads[$id].progress = $progress
                    | .beads[$id].state = $next
                    | .beads[$id].seen_run = .runs
                    | .beads[$id].seen = $e.ts
                    | .beads[$id].title = (if $title == "" then (.beads[$id].title // "") else $title end)
                  end
              end
        )
    # Step 4: prune. Closed beads are gone already. Drop the quiet ones too, so
    # the ledger stays the size of what is actually moving. This replaced one
    # `gc bd show` call per tracked bead, which was the slowest part of the old
    # script and is unnecessary now that closes arrive as events.
    | .runs as $runs
    | .beads |= with_entries(select($runs - (.value.seen_run // 0) <= $retain))
    ')

# Step 5: mail once per NEW no-progress reset at or past the threshold. A run
# that saw no new reset sends nothing, however long the bead has been sitting
# there, and a bead whose work moved is back at zero and says nothing at all.
#
# Only a mail that really went is written down as sent. `gc mail send` can
# return 0 and still not deliver under Dolt load, and only a printed
# "Sent message <id>" proves it went (Blue-Scroll core safety rule). A mail
# that did not go leaves "mailed" alone, so the next run tries again instead
# of losing the alarm.
MAILED_OK="$(mktemp)"
trap 'rm -f "$MAILED_OK"' EXIT

send_storm_mail() { # bead-id title count progress-line -> 0 when the mail went
    local bead_id="$1" title="$2" count="$3" progress="$4" out="" subject="" body=""
    subject="SPAWN_STORM: bead $bead_id reset ${count}x with no progress"
    body="Bead $bead_id ($title) went back to the pool $count times in a row and
NOTHING about the work moved in between (threshold: $THRESHOLD).

What did not move, on every one of those $count rounds:
  $progress

That line is the bead's branch, the sha the refinery last judged, and its PR.
A refusal-and-fix round changes at least one of them, and this bead changed
none, which is the shape of a seat that claims the work and dies.

What to do:
- Look at the bead: gc bd show $bead_id --json
- See who claimed it and when: gc bd history $bead_id --events
- See why it was let go: metadata.rejection_reason
- Consider quarantining the bead or fixing the cause."
    if [ -n "$MAIL_SINK" ]; then
        # The sink writes the REAL subject and body, not a second string built
        # beside them. A sink with its own wording lets a test pass while the
        # mayor reads something else. Newlines become spaces so one mail stays
        # one line; every word survives.
        printf '%s\t%s\t%s\t%s\n' "$bead_id" "$count" "$subject" \
            "$(printf '%s' "$body" | tr '\n' ' ')" >> "$MAIL_SINK"
        return 0
    fi
    out=$(gc mail send mayor/ -s "$subject" -m "$body" 2>&1) || true
    case "$out" in
        *"Sent message"*) return 0 ;;
    esac
    echo "spawn-storm-detect: mail for $bead_id did NOT go, will retry next run: $out" >&2
    return 1
}

STORMS=0
while IFS=$'\t' read -r bead_id count title progress; do
    [ -z "$bead_id" ] && continue
    if send_storm_mail "$bead_id" "$title" "$count" "$progress"; then
        printf '%s\n' "$bead_id" >> "$MAILED_OK"
        STORMS=$((STORMS + 1))
    fi
done < <(printf '%s\n' "$NEXT_LEDGER" | jq -r --argjson thr "$THRESHOLD" '
    .beads | to_entries[]
    | select((.value.resets // 0) >= $thr and (.value.resets // 0) > (.value.mailed // 0))
    | [.key, (.value.resets // 0), (.value.title // "unknown"),
       (if (.value.progress // "") == "" then "nothing recorded" else .value.progress end)]
    | @tsv')

# Step 6: write down the mails that went, then save.
#
# Save through a temp file. Redirecting straight onto the ledger empties it
# before jq runs, so a jq failure here would throw away the counts instead of
# keeping them.
LEDGER_TMP="$(mktemp "${LEDGER}.XXXXXX")"
trap 'rm -f "$MAILED_OK" "$LEDGER_TMP"' EXIT
printf '%s\n' "$NEXT_LEDGER" | jq -c --rawfile sent "$MAILED_OK" '
    ($sent | split("\n") | map(select(. != ""))) as $ok
    | .beads |= with_entries(
        if (.key | IN($ok[])) then .value.mailed = (.value.resets // 0) else . end)' > "$LEDGER_TMP"
mv "$LEDGER_TMP" "$LEDGER"

if [ "$STORMS" -gt 0 ]; then
    echo "spawn-storm-detect: mailed $STORMS beads that went back to the pool past the threshold with no progress"
fi
