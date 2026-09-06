# `batuta loop` — the mechanical conductor on file hosts

`batuta loop` runs an approved plan (`.batuta/plan-<slug>.md`) without a
model in the conductor's seat. It is the Ralph loop of the
beer-and-code-harness driven by the core delivery graph instead of a phase
list, with the same invariants:

1. Every task and every fix cycle runs in a **new executor session** with a
   self-contained brief. Nothing is reused across sessions.
2. **Zero questions** by default. An executor that must stop prints one
   `BATUTA-QUESTION: <text>` line; the task parks and the run ends with
   `waiting_input` until `--answer` brings the text back.
3. A task is done only when it passes the **four gates**, never on the
   executor's exit code or report.
4. A **usage limit is not a failure**: the loop waits for the reset and runs
   the same attempt again, spending no retry and no escalation.
5. **One commit per task**, integrated onto the branch that was checked out
   when the delivery opened.

## Progress protocol

An executor session may stream progress to the loop with plain-text lines on
stdout. For each acceptance criterion `n`, the executor prints one isolated
line `BATUTA-PROGRESS <n> START` before the first edit for that criterion and
`BATUTA-PROGRESS <n> DONE` when the criterion's proof passes locally. The line
must stand alone: no prefix, suffix, or extra text on the same line.

## Packages

| Package | Role |
|---|---|
| `journal` | Append-only JSONL per delivery under `.batuta/journal/`, hash-chained; every record carries the graph after the transition |
| `worktree` | `GitProvider`: worktrees under `.batuta/worktrees/`, squash, bookkeeping commits, `.git/info/exclude` |
| `executor` | Adapter frontmatter → argv (no shell), subprocess with stdin closed, timeouts, process-group kill, `finished` and `limit_regex` rules, question line |
| `gates` | Gate 0 finished · 1 tree · 2 tests · 3 scope, proofs, independent read-only verifier |
| `loop` | The runner over `routing.DeliveryGraph`: routing from the table, waves, attempts, retry then escalation, integration, bookkeeping, resume, answer, abandon, dashboard, trail |

## One run

```
preflight   profile (Test: line), routing table, approved plan, skills with
            adapters/, clean tree (managed state included), checked-out
            branch, Scope entries contained in the repository, no task
            routed to `self`
routing     routing.RoutingTable.Generation over the inventory → a frozen
            RoutingGeneration recorded in the delivery_opened record
loop        AdmitReadyWave → BeginWaveAttempts → attempts in parallel (at
            most 4, `Execution:` line or --parallel) → settle every wave
            that holds candidates → repeat until done, blocked or waiting
attempt     worktree at the attempt's base → optional Install: → brief →
            executor via adapter → gates → squash to one conventional
            commit → integration.GitClient.Candidate → RecordCandidate
            (or RecordFailureWithPolicy: retry same runtime in the same
            worktree with the failure as feedback, then one escalation in
            a fresh worktree, then abort)
settle      integration.GitClient.Preflight in a disposable worktree →
            Apply per accepted candidate on the branch → SettleWave →
            cleanup; a conflicting candidate re-executes on the new base
terminal    done | blocked | waiting_input | canceled | abandoned
```

Exit codes: `0` done · `2` blocked · `3` waiting for an answer · `130`
canceled · `1` an error before or during the run.

## Standalone gates

The interactive skill can run each verification gate independently. Every
gate writes compact JSON followed by a newline and no other stdout. A passing
verdict exits `0`, a failing verdict exits `2`, and usage or runtime errors
exit `1` with the reason on stderr.

- `batuta gate tree --snapshot [--dir <d>]` captures the current tree, while
  `batuta gate tree --before '<json>' [--dir <d>]` compares it with a prior
  snapshot. `--dir` defaults to the current directory.
- `batuta gate tests --command "<cmd>" [--dir <d>] [--timeout <duration>]`
  runs the test command with a default timeout of 15 minutes.
- `batuta gate scope --base <sha-or-ref> --scope <a,b,c> [--dir <d>]`
  checks changed paths against the comma-separated scope. An empty scope is
  allowed, and the output also identifies outside and managed paths.
- `batuta gate proofs --accept "<criterion → proof>;..." [--dir <d>] [--timeout <duration>]`
  runs the declared proof commands and returns a JSON array of verdicts.
  Criteria without an arrow are left to the verifier.
- `batuta gate verifier --criteria <n> [--proofs '<json array>'] < output`
  reads the verifier output from stdin and checks it against the criterion
  count and optional proof verdicts.

## Decisions

- **Journal authority.** On file hosts the delivery journal is the single
  source of truth for a delivery; `--resume` loads the last record's graph
  and verifies the chain. The routing ownership store
  (`routing/ownership.go`) stays the daemon's; the loop never writes it.
- **Progress is journaled.** `task_progress` records capture streamed
  progress from executor sessions with `execution`, `criterion`, and
  `state` fields; the record timestamp is the event time, and the record
  carries the same graph as every other journal entry.
- **Routing comes from the table.** A plan's `→ executor/model` hint is
  reported in `--dry-run` when it disagrees with the table and otherwise
  ignored: the user's table is the routing decision (core #18, task
  overrides). `reasoning` follows the lane (`low|medium|high|xhigh`).
- **`self` has no seat in the loop.** A task whose selected row is `self`
  fails the preflight with the instruction to run it interactively through
  `/batuta` and tick it. A task that would *escalate* to `self` is aborted
  with blocker `needs_conducting_session` (core #18, self handoff).
- **Criterion syntax.** `Accept: <criterion> → <proof>; …` where the proof
  is a command run in the worktree with `sh -c`; exit 0 means the
  criterion holds. A criterion without an arrow has no mechanical proof
  and is left to the verifier. Entries split on `;`, so a proof may not
  contain one (core #18, criteria). `Scope:` entries must be contained in
  the repository (no absolute path, no `..`); a changed path matches an
  entry as an exact path, a directory prefix or a glob (`**` crosses
  directories).
- **The integration chain is contiguous.** The next settlement starts at
  the previous settlement's final head. The loop therefore commits nothing
  to the branch between waves — WORK.md lines and plan ticks are written
  and committed once, at a final state (`done`, `blocked`, `abandoned`).
  A branch that moved outside the loop is refused on `--resume` with the
  advice to `--abandon` and start a new delivery; ticked tasks carry over.
- **A silent session is a signal.** Gate 1 unchanged and the criteria hold
  on the base per gates 2 and 3 → the task is *already satisfied*: no
  candidate, no commit, ticked in the plan at the end. Unchanged and the
  criteria do not hold → failure (`no_changes`).
- **Same-runtime retry keeps the worktree**, so the fix session sees the
  partial work and the brief carries the real cause. An escalation starts
  clean.
- **User-authored command lines** (`Test:`, `Install:`, proofs) run through
  `sh -c` with stdin closed, a timeout and bounded output; they come from
  files the user wrote and approved. **Executor lines never see a shell**:
  the adapter's `run` is tokenized once, placeholders are substituted per
  token, and shell syntax in an adapter line is a parse error.
- **Interrupted attempts** (a killed loop) are recorded as `stalled` with
  blocker `interrupted` on `--resume`; the conducting policy then retries in
  the same worktree.
- **Verifier.** The `low` row's executor of the task's domain when it
  differs from the one that wrote the diff, else the task's own adapter;
  invoked through the adapter's `readonly` line with the headless contract
  (no background work, quick synchronous commands only, `TASK n:
  DONE|INCOMPLETE` mandatory). Any tree change during the verifier round
  invalidates it.
- **Dashboard watch.** `--dashboard` with `--watch` renders a live panel
  every interval, clearing the screen before each redraw. The watch mode
  follows the most recent open delivery when no delivery argument is given,
  exits cleanly when there are no open deliveries, and stops at the terminal
  record or on cancellation without writing to the journal.

## Files the loop writes

| Path | Tracked | When |
|---|---|---|
| `.batuta/journal/<delivery>.jsonl` | no (`.git/info/exclude`) | every transition |
| `.batuta/worktrees/<slug>-task-N-e<k>/` | no | per attempt; removed after integration or abort (`--keep-worktrees` keeps them) |
| `.batuta/runs/<date>-<slug>-task-N.md` (+ `-e<k>.brief.md`, `-e<k>.out.log`) | no | per attempt |
| `.batuta/asks/<slug>-task-N.md` | no | when a task asks; removed by `--answer` |
| `WORK.md`, `.batuta/plans/<slug>.md` | yes | once, at a final state, in one `chore(batuta): <slug> — loop <state>` commit |
| `.batuta/plans/done/<slug>.md` | yes | when all tasks are done; the bookkeeping commit carries the plan move |

Legacy `.batuta/plan-<slug>.md` plans remain readable for one release. The
active path takes precedence when both exist. Unfinished plans stay at their
loaded location; finished plans move to `.batuta/plans/done/` and are excluded
from plan discovery.

## Not in this release

- Token accounting: CLI executors do not report tokens, so the graph's
  budget is unused; the wall budget is `--task-timeout` per session.
- Cross-review with lenses (the skill's `/batuta-review`); the loop runs the
  independent verifier only.
