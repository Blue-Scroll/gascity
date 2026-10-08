# Turn-bound claims — operator notes

`gc hook --claim` refuses to mint a claim that no agent turn will consume. This
page covers the two knobs and the two failure shapes an operator sees as a
result. Background and rationale: ga-fylee.

## The fences, in one paragraph

A claim is refused outright when the invocation looks like a provider callback
rather than a turn (F-A), or when the invocation has outlived its claim window
(F-B). A claim that is won but cannot be delivered — the provider closed the
tool pipe, or the compare-and-swap landed after the window closed — is released
again and reported as `bead.claim_released` (F-C). A session that acknowledges
drain gives back any `in_progress` claim it is still holding (F-D).

## `GC_HOOK_CLAIM_WINDOW` — the one tuning knob

It sets the **base** window: how long after `gc hook --claim` starts a claim
mutation may still run. Past the window the command exits 1, emits
`execution.claim_window_expired`, and deliberately writes **no drain record**.
A spent window is a dead invocation, not an idle store.

The default base is `hookWorkQueryTimeout + hookClaimMutationTimeout`, which is
**2m40s** today (150s + 10s). It is derived, not a literal, so it moves when
either budget is retuned.

**A live parent stretches the window 4x** (`hookClaimLiveParentWindowFactor`),
to 10m40s by default. "Live" means the process that started `gc hook --claim`
is still its parent. One claim can run several work queries in a row (one per
federated store, a re-read, and retries on a failed read), and each may take the
full 150s. So under store load the reads alone can use up the base window while
the turn is still waiting. Before the stretch, that refused every claim the
command had just found (vn-buvmt78: refusals at 2m41s and 3m1s, from a turn
with a 10 minute tool timeout). The stretch has a ceiling because a provider
host can stop reading a slow command and stay alive (ga-fylee).

An orphaned claimer keeps the base window. It is detected by its parent pid
changing, not by the parent being pid 1, so an orphan reparented to a subreaper
inside a container is caught too.

Reading the alarm, `execution.claim_window_expired`:

- `parent_alive=false`: the claimer was orphaned by a dead provider tool call.
  That is the fence working.
- `parent_alive=true`: a live turn waited past even the stretched window. Either
  the store is far too slow (fix the store), or the provider stopped reading the
  command. A steady stream of these means real claims are being refused.

Setting `GC_HOOK_CLAIM_WINDOW` below the default makes the fence stricter; the
4x stretch scales with it. Do not lower it to "fix" a starved pool: that is the
opposite of what a starved pool needs.

The window can exceed `idleClaimNudgeGrace` (90s, `cmd/gc/idle_nudge.go`). A
claim still reading when the backstop fires only earns the backstop's next
idempotent re-nudge, never a double claim. The comment on
`hookClaimWindowDefault` in `cmd/gc/cmd_hook_claim.go` has the reasoning.

## Failure shape: a leaked marker fences the whole fleet

F-A keys on environment markers gc sets on its own callback lanes:
`GC_HOOK_CALLBACK_LANE`, `GC_MANAGED_SESSION_HOOK`, `GC_HOOK_EVENT_NAME`. They
are per-command prefixes on those lanes and are never part of a turn's
environment.

If an operator exports any of them **fleet-wide** — a shell profile, a systemd
unit, a container env — every claim in every session is refused. The symptom is
distinctive and easy to misread: workers exit **0**, report a clean drain with
`reason=non_turn_context`, and simply never pick up work. It looks like an idle
city, not a broken one.

The refusal names the offending variable on stderr:

```
gc hook --claim: refusing to claim from a non-turn context (GC_HOOK_EVENT_NAME
is set); a provider callback's result reaches no agent turn, so a claim minted
here would be parked the instant it is won
```

So the diagnosis is `env | grep -E 'GC_HOOK_CALLBACK_LANE|GC_MANAGED_SESSION_HOOK|GC_HOOK_EVENT_NAME'`
inside a stuck worker's session. Unset it there; do not add exceptions to the
fence.

## Failure shape: a released claim after a started step

`bead.claim_released` on a bead that already has `execution.step_started` is a
**compensation pair**, not a step that ran. The claim path stamps the step at
claim time and only then discovers it cannot deliver the result. Read the pair
as "no attempt happened". Anything consuming the execution event stream must
not leave such a step in flight waiting for a `execution.step_completed` that
is never coming.

## What is NOT fenced, on purpose

- **Adoption** of work the session already holds. It mints no new obligation,
  and a re-woken holder must still be able to resume.
- **`gc agent script`** deterministic executors. They claim through the raw `bd`
  binary and execute in the same process, so they have no turn to outlive.
- **`gc bd update --claim`.** Worker-pull, and no shipped prompt uses it to
  acquire work; a test pins that so it cannot silently become load-bearing
  while unfenced.
