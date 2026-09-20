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
THRESHOLD="${SPAWN_STORM_THRESHOLD:-2}"

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
#   { "version": 2,
#     "runs": <how many times this script has completed>,
#     "cursor_seq": <highest event seq already counted>,
#     "beads": { "<id>": { "state": "held" | "free",
#                          "resets": <real returns to the pool>,
#                          "mailed": <resets already reported>,
#                          "seen_run": <run number of its last event>,
#                          "seen": "<timestamp, for humans only>",
#                          "title": "<string>" } } }
#
# A version 1 ledger is a flat {"<id>": <int>} map of the OLD run counts.
# Those numbers mean nothing, so they are thrown away rather than carried
# forward into a field that now means something else.
LEDGER_JSON='{"version":2,"runs":0,"cursor_seq":0,"beads":{}}'
if [ -f "$LEDGER" ]; then
    LOADED=$(jq -c 'if type == "object" and (.version? == 2) then . else null end' "$LEDGER" 2>/dev/null || echo null)
    if [ -n "$LOADED" ] && [ "$LOADED" != "null" ]; then
        LEDGER_JSON="$LOADED"
    else
        echo "spawn-storm-detect: ledger reset (old run-count format, see hq-2yztr2)" >&2
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

# Step 3: walk the events in sequence order and count real resets.
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
            | if $e.seq <= .cursor_seq or $next == "unknown" then .
              else
                .cursor_seq = $e.seq
                | if $next == "closed" then del(.beads[$id])
                  else
                    .beads[$id].resets = ((.beads[$id].resets // 0)
                        # MUTANT ANCHOR (do not reflow): the whole fix is this
                        # one test. Counting a "free" sighting instead of a move
                        # INTO free is the old bug. --self-test rewrites this
                        # line to that broken form and requires the rows to fail.
                        + (if ($prev == "held" and $next == "free") then 1 else 0 end))
                    | .beads[$id].mailed = (.beads[$id].mailed // 0)
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

# Step 5: mail once per NEW reset at or past the threshold. A run that saw no
# new reset sends nothing, however long the bead has been sitting there.
#
# Only a mail that really went is written down as sent. `gc mail send` can
# return 0 and still not deliver under Dolt load, and only a printed
# "Sent message <id>" proves it went (Blue-Scroll core safety rule). A mail
# that did not go leaves "mailed" alone, so the next run tries again instead
# of losing the alarm.
MAILED_OK="$(mktemp)"
trap 'rm -f "$MAILED_OK"' EXIT

send_storm_mail() { # bead-id title count -> 0 when the mail really went
    local bead_id="$1" title="$2" count="$3" out=""
    if [ -n "$MAIL_SINK" ]; then
        printf '%s\t%s\n' "$bead_id" "$count" >> "$MAIL_SINK"
        return 0
    fi
    out=$(gc mail send mayor/ \
        -s "SPAWN_STORM: bead $bead_id reset ${count}x" \
        -m "Bead $bead_id ($title) has gone back to the pool $count times (threshold: $THRESHOLD).
Each count is one real move from claimed to open-and-unassigned, read from the
city event stream. A seat is most likely crash looping on this work.

What to do:
- Look at the bead: gc bd show $bead_id --json
- See who claimed it and when: gc bd history $bead_id --events
- See why it was let go: metadata.rejection_reason
- Consider quarantining the bead or fixing the cause." 2>&1) || true
    case "$out" in
        *"Sent message"*) return 0 ;;
    esac
    echo "spawn-storm-detect: mail for $bead_id did NOT go, will retry next run: $out" >&2
    return 1
}

STORMS=0
while IFS=$'\t' read -r bead_id count title; do
    [ -z "$bead_id" ] && continue
    if send_storm_mail "$bead_id" "$title" "$count"; then
        printf '%s\n' "$bead_id" >> "$MAILED_OK"
        STORMS=$((STORMS + 1))
    fi
done < <(printf '%s\n' "$NEXT_LEDGER" | jq -r --argjson thr "$THRESHOLD" '
    .beads | to_entries[]
    | select((.value.resets // 0) >= $thr and (.value.resets // 0) > (.value.mailed // 0))
    | [.key, (.value.resets // 0), (.value.title // "unknown")] | @tsv')

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
    echo "spawn-storm-detect: mailed $STORMS beads that went back to the pool past the threshold"
fi
