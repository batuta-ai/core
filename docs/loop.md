# `batuta loop` — the mechanical conductor on file hosts

`batuta loop` runs an approved plan (`.batuta/plans/<slug>.md`) without a
model in the conductor's seat. It is the Ralph loop of the
beer-and-code-harness driven by the core delivery graph instead of a phase
list, with the same invariants:

1. Every task and every fix cycle runs in a **new external executor session**
   with a self-contained brief. The transport is the legacy CLI or a qualified
   ACP session; native host children are not available to the headless loop.
   Nothing is reused across sessions.
2. **Zero questions** by default. An executor that must stop prints one
   `BATUTA-QUESTION: <text>` line; the task parks and the run ends with
   `waiting_input` until `--answer` brings the text back.
3. A task is done only when it passes the **four gates**, never on the
   executor's exit code or report.
4. A **usage limit is not a failure**: the loop waits for the reset and runs
   the same attempt again, or switches to its next runtime when the wait
   budget is exhausted, spending no retry and no escalation.
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
| `executor` | Adapter frontmatter → argv (no shell), subprocess with stdin closed, timeouts, process-group kill, `finished`, `limit_regex` and `usage_regex` rules, question line |
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
            executor via selected external transport → gates → squash to one conventional
            commit → integration.GitClient.Candidate → RecordCandidate
            (or RecordFailureWithPolicy: retry same runtime in the same
            worktree with the failure as feedback, then one escalation in
            a fresh worktree, then abort)
settle      integration.GitClient.Preflight in a disposable worktree →
            Apply per accepted candidate on the branch → SettleWave →
            cleanup; a conflicting candidate re-executes on the new base
terminal    done | blocked | waiting_input | canceled | abandoned
```

Exit codes: `0` implementation done and required review cleared · `2` blocked
or `review_blocked` · `3` waiting for an answer · `4` waiting for an approved
roadmap plan · `130` canceled · `1` an error before or during the run.

## After the run

Normal new, resume, answer and roadmap execution includes passive foreground
supervision and full review of the recorded final commit against the original
base/spec after finalization. Worker/progress output remains on stdout;
structured observer reports go to stderr. Review runs without `--policy`;
questions and corrections gain no automatic authorization.

Implementation `done` is separate from review. Only intact complete `SHIP`
evidence or valid explicit digest-bound operator judgment clears progression.
Findings, missing or failed review, incomplete coverage and uncertain execution
block later roadmap phases durably. A pending gate returns `review_blocked`
(exit `2`); runtime/evidence errors return `1`. Resume with
`batuta loop --resume <delivery>` or rerun `--roadmap`; neither resets the review
budget nor replays uncertain jobs. Review artifacts remain evidence for
conductor acceptance and grant no permission to merge or publish. Attach the
review artifacts to the PR after the conductor's decision.

Use `batuta loop --supervise <delivery> --review-status` to inspect the exact
progression digest without execution. Explicit judgment uses `--review-judgment
accept|reject --review-id <id> --review-digest <sha256:digest> --rationale
"<reason>"` with the same `--supervise <delivery>`. See
[loop-supervision.md](loop-supervision.md) for full commands, recovery constraints,
policy and notification details, and [review.md](review.md) for the review contract.

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

After `report.Decide()`, the loop may ask the optional `claim_evidence`
decision: two `noul` questions (`claim_unsupported`, `verifier_contradicted`)
over a bounded state of the plan criteria, executor report, tree, proofs and
verifier. `shadow` records `judge_intent`/`judge_result` and leaves the
attempt unchanged. `enforce` may only fail a passing attempt, and only when
either probability is at or above the decision threshold (default 0.9), with
blocker `claim_unsupported`; it never turns a failure into a pass. A missing
or unavailable judge keeps the deterministic gates. Attempts that ended in a
question, a rate limit, an executor error or a reconciliation block are not
asked.

## Decisions

- **External transport is opt-in.** `--transport cli|acp|auto` selects the
  task transport and defaults to `cli`. `acp` fails before submission when no
  exact qualification exists. `auto` can fall back to CLI before submission,
  but never after an ACP prompt may have run. The source constructor qualifies
  only OpenCode 1.18.31 with `opencode acp`, model `opencode/big-pickle` and
  empty effort on native macOS arm64. Every other OS/architecture, including
  macOS amd64 (Intel or Rosetta), Linux and Windows, remains on CLI.
  Native host dispatch belongs to an interactive host and is not a loop
  transport. The independent verifier
  remains a separate CLI session regardless of task transport. See
  [dispatch.md](dispatch.md).

- **Journal authority.** On file hosts the delivery journal is the single
  source of truth for a delivery; `--resume` loads the last record's graph
  and verifies the chain. The routing ownership store
  (`routing/ownership.go`) stays the daemon's; the loop never writes it.
- **Progress is journaled.** `task_progress` records capture streamed
  progress from executor sessions with `execution`, `criterion`, and
  `state` fields; the record timestamp is the event time, and the record
  carries the same graph as every other journal entry.
- **Supervision is foreground and enabled for normal execution.**
  `batuta loop --supervise <delivery> --cursor <absolute-path>` observes one
  delivery with a durable outbox and no
  model calls while waiting. An explicit `continue_approved_task` policy can
  answer and resume its assigned task with normal runner settings; resumed
  completion enters automatic review. Correction policies reserve a proposal
  without starting a runner. A local file or desktop sink may be configured;
  otherwise events remain unread. The process must remain running, and no chat
  turn, remote message, or exactly-once notification is implied. See
  [loop-supervision.md](loop-supervision.md) for lifecycle, policy, host-limit,
  evidence, cancellation, and cost-accounting details. The CLI review timeout
  is fixed at one hour: ownership-wait interruption returns before a job
  transition, and pre-launch probe/snapshot interruption records
  `failed`/`execution_failed`. Cancellation or timeout while the engine command
  runs records `uncertain`/`cleanup_unresolved`; post-exit verification can
  instead record `failed`/`execution_failed`. Recovery from a durable
  `launching` state records `uncertain` without necessarily setting an outcome.
  Neither uncertain case is replayed automatically, and unsupported hosts do
  not claim verified descendant cleanup.
- **Decisions may be task-scoped.** In `## Decisions and context`, a paragraph
  beginning with `**Task N.**` belongs only to task N; `**Tasks N–M.**` (also
  `N-M`, comma lists, and combinations) belongs to every named task. Unlabelled
  paragraphs are shared. Each executor brief keeps shared and its own marked
  paragraphs in plan order and drops paragraphs marked for other tasks.
- **Routing comes from the table.** A plan's `→ executor/model` hint is
  reported in `--dry-run` when it disagrees with the table and otherwise
  ignored: the user's table is the routing decision (core #18, task
  overrides). `reasoning` follows the lane (`low|medium|high|xhigh`).
- **Usage-limit fallback.** The legacy CLI policy is unchanged:
  `--max-limit-waits` (default 20) bounds the waits in one attempt. At that
  cap, or when a named reset is more than
  `--limit-horizon` (default `2h`) away, the loop walks to the cell's next
  executable fallback, using the same cell walk as escalation. It reruns the
  brief in the same worktree with the same execution number and run ID;
  partial work stays available and no retry or escalation is spent. The wait
  count stays with the attempt across runtime switches. A reset within the
  horizon still waits until reset plus the buffer; an unnamed reset uses
  `--limit-wait` (default `30m`). With no executable fallback left (including
  `self`), the loop waits the remaining budget, then blocks `rate_limited`.
  Each switch journals `limit_fallback` with `execution`, `from`, `to`,
  `reset_at`, and `waits`. The trail shows the switch and watch shows the new
  runtime without incrementing retries or escalations. `--dry-run` lists the
  next limit fallback per task, or `none`. Proposal #54's separate **Limit
  fallback** routing-table column is deferred; no new column is required.
  An ACP quota response after possible prompt submission is uncertain work,
  not proof of non-execution, so the loop parks it for reconciliation without
  a fallback, retry or escalation.
- **Executor telemetry in the journal.** An unclean invocation — non-zero
  exit, timeout, rate limit or unfinished turn — records `output_tail` in its
  `executor_finished` and `limit_wait` records: the last 40 lines of each
  stream, each cut to its final 4096 bytes on a UTF-8 boundary, with
  workspace paths redacted and secret-shaped lines dropped before it reaches
  the journal. Clean sessions carry no tail. An adapter may also declare
  `usage_regex`, a case-insensitive pattern naming any of the counters
  `input`, `output`, `cached` and `total` (it must compile and name at least
  one, or the adapter is invalid). The loop applies it to the same last
  20 lines of stdout and stderr `limit_regex` uses and records what it
  captured as `usage` on `executor_finished` with provenance
  `cli/usage_regex`; thousands separators (comma, dot, thin space) are
  parsed away, unmatched counters stay nil, and a CLI that prints only a
  total (e.g. `tokens used\s+(?P<total>[0-9][0-9., ]*)`) sets
  `reported_total_tokens` without inventing input or output. A session with
  no reported usage records `usage_unknown` instead.
- **Conflicts keep the same runtime.** A conflicting candidate re-executes on
  the new base with the same executor, model, and reasoning; escalation is
  reserved for verification failures.
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
  on the base — the test command, every proof command and the read-only
  verifier all pass there — → the task is *already satisfied*: no
  candidate, no commit, ticked in the plan at the end. Unchanged and any
  of them fails → failure (`no_changes`) with the failed proof as feedback.
- **Same-runtime retry keeps the worktree**, so the fix session sees the
  partial work and the brief carries the real cause. An escalation starts
  clean.
- **Failure outcomes.** The ordinary conducting policy retries once on the
  same runtime with feedback, escalates once in a fresh worktree, then blocks
  the task; other ready tasks and later waves continue whenever their
  dependencies permit. The blocker tells the operator why:
  - `timed_out` marks the attempt stalled, then follows the ordinary policy.
  - `verifier_incomplete`, `tests_failed`, `scope_violation`, `no_changes`,
    `candidate_invalid`, `question_unsafe`, and `install_failed` follow the
    ordinary policy. So do `executor_failed` and `proof_failed`, the remaining
    executor and gate blocker codes.
  - `interrupted` is written as stalled when `--resume` finds an attempt that
    was still running, then follows the ordinary policy in its preserved
    worktree.
  - `needs_conducting_session` blocks immediately when the next escalation is
    `self`; the task must be completed through an interactive conducting
    session and then ticked or replanned.
  - `question_at_ceiling` records the question and blocks immediately instead
    of creating an impossible continuation; answer it by hand and replan.
  - `already_satisfied` is a successful no-commit outcome: gates 2 and 3 hold
    against the attempt base, the task is marked integrated at that base, and
    the delivery continues to any newly ready dependents.
  - `rate_limited` spends neither retry nor escalation. The loop waits or uses
    the `limit_fallback` described above; only after the wait budget and all
    executable fallbacks are exhausted does it block.
- **Work is snapshotted before it can be discarded.** Before a question parks
  an attempt, a usage-limit wait or fallback, any recorded failure or
  interruption, worktree cleanup, and every terminal delivery record, the
  loop snapshots tracked, staged, unstaged, and untracked executor work to
  `refs/batuta/parked/<slug>/<task>-e<execution>`. The synthetic commit is
  named `wip(batuta): <slug> <task> e<execution> parked`, leaves the real HEAD,
  index, and files untouched, and is journaled as a `worktree_snapshotted`
  record. A same-runtime retry keeps the worktree; a fresh escalation or
  cleanup may remove it only after that snapshot succeeds. Finalization first
  computes which refs to retain, then journals that list in the terminal
  record, and only then deletes refs whose complete tree is already present
  in branch history, in the same critical section. `delivery_terminal` stays
  last and carries both the retained refs and the deletion plan. Failures print
  the error and remaining refs (all planned deletions if relisting fails).
  The next `--resume` or `--abandon` checkpoints and retries those deletions
  before writing another terminal record, without repeating task work or
  bookkeeping. This also recovers interruption between recording and deletion;
  an already completed deletion is safe to retry.
  An unmerged (conflicted) index is copied and serialized into separate trees:
  `-index` retains normal stage-zero entries, and `-index-stage-1`,
  `-index-stage-2`, and `-index-stage-3` retain the conflicted base, ours, and
  theirs entries at their original paths, including file modes. Absent stages
  need no tree. Only unmerged entries are streamed to a temporary file and
  reconstructed in bounded batches; ordinary indexes use `write-tree` on a
  copy without enumerating stage-zero entries. These recovery refs protect
  staged blobs without resolving or
  changing the real index; the working-directory snapshot still includes the
  unresolved file contents. The summary lists conflicted paths for retained
  stage refs. Use `git show <ref>:<path>` to recover a specific version.
- **Finalization can be retried.** A `delivery_finalizing` checkpoint records
  the result, original plan path, recovery refs, and pending cleanup and
  bookkeeping before worktrees are removed or the plan is archived. Cleanup
  and bookkeeping completion are tracked separately. If archival, staging,
  or the bookkeeping commit fails, the journal records the error and the
  summary prints `batuta loop --resume <delivery>` and
  `batuta loop --abandon <delivery>` recovery commands. Either command retries
  finalization with its original result without running tasks again. Recovery
  accepts a plan already moved into `plans/done/`, repeats any unfinished
  staging and commit, and refuses staged paths outside the source/archived
  plan, WORK.md, and roadmap before changing bookkeeping files. Unstage any
  unrelated paths before retrying. Recovery avoids duplicate WORK.md entries or bookkeeping
  commits, including interruption after a successful commit. A terminal
  record is appended only after bookkeeping succeeds; a separate checkpoint
  preserves that success if the final append is interrupted. Cleanup failures
  remain retryable through the same commands. The presence heartbeat uses
  the runner's clock and cancellable sleep, so tests can drive refreshes
  without waiting for wall time. Acquisition and takeover use the guard
  lock directly and have no timed retry loop.
- **WORK.md is generated bookkeeping, not approval evidence.** At finalization,
  the loop derives entries from the delivery summary and journal, writes them
  with the plan ticks, and commits both once. A `done` entry records
  implementation completion; review acceptance is a later, separate conductor
  decision. Existing dirty managed files still fail loop preflight and must be
  committed before a new delivery.
- **User-authored command lines** (`Test:`, `Install:`, proofs) run through
  `sh -c` with stdin closed, a timeout and bounded output; they come from
  files the user wrote and approved. **Executor lines never see a shell**:
  the adapter's `run` is tokenized once, placeholders are substituted per
  token, and shell syntax in an adapter line is a parse error.
- **Interrupted attempts** (a killed loop) are recorded as `stalled` on
  `--resume`. A legacy CLI attempt follows its existing retry policy in the
  preserved worktree. An ACP intent that may have submitted blocks as
  `submission_uncertain`; it is not replayed until the preserved work is
  reconciled.
- **Verifier.** Selects the first loadable adapter with an executor different
  from the writer's, starting with the research row of the task's lane and
  descending through lower research rows, then the implementation `low` row
  of the task's domain, falling back to the task's own adapter and model.
  Invoked through the adapter's `readonly` line with the headless contract
  (no background work, quick synchronous commands only, `TASK n:
  DONE|INCOMPLETE` mandatory). Any tree change during the verifier round
  invalidates it.
- **Unsigned child sessions.** Executor and verifier sessions receive
  `commit.gpgsign=false` through Git's command-line configuration environment.
  The loop's integration commit does not receive that override and therefore
  keeps the user's signing configuration.
- **Dashboard watch.** `batuta watch [<delivery>]` opens the live dashboard;
  `batuta loop --dashboard --watch` is the equivalent loop form. `PanelModel`
  is the pure journal-to-view projection and `Render` is the pure
  view-to-frame renderer. The Bubble Tea program owns terminal size, focus,
  navigation and animation, and its poller checks the journal and selected
  task's run log for changes. `--interval` sets that journal and run-log poll
  interval (500ms by default); unchanged polls do not redraw. The program also
  redraws for keys, window-size events, the one-second clock, and active
  animation ticks. It follows the most recent open delivery when none is
  named and never exits on its own, including when the delivery reaches a
  terminal state; use `q` or Ctrl+C to leave it. `--once` calls `Render`
  directly for one non-interactive frame. When stdin is not a TTY, watch falls
  back to plain, colourless snapshots on journal changes and never starts
  Bubble Tea.
  The display has a delivery/branch/state header and attention line, Context
  and Progress panels (including completion bars), a Detail panel for the
  selected task, waves with task rows, and a live tail of that task's executor
  log. Each task row shows status, attempt, the four gate results in one Gates
  column (`G0` executor finished, `G1` tree changed, `G2` tests, `G3` scope,
  proofs, and independent verification), and its commit. Use Up/Down and
  PgUp/PgDn to scroll, `f` to follow the active task, `r` to open the
  multi-line answer editor for a waiting task, `R` to show its shell answer
  command, `d` to open the delivery picker without leaving the watch, `o` to
  open the selected log with `$PAGER`, `?` for the legend, `l` to move focus
  between the task table and run log, and `q` (or Ctrl+C) to quit. In the
  answer editor, Enter submits the answer; `ctrl+j` or `shift+enter` inserts a
  newline; `ctrl+d`, `ctrl+enter`, `alt+enter`, and `ctrl+s` also submit; and
  Esc cancels. A submitted answer resumes the loop as
  a detached process and writes its output to
  `.batuta/runs/loop-<delivery>.log`. The mouse wheel scrolls whichever panel
  has focus; Up/Down and PgUp/PgDn do the same, and End returns the focused log
  to its live tail.
  Each running loop refreshes `.batuta/journal/<delivery>.lock`. The header
  shows `loop ●` when the displayed delivery has a fresh lock, `loop ○` when
  it has none, and `loop ○ stale` when its lock is no longer fresh; when
  more than one fresh lock exists, it also shows the workspace-wide loop
  count. Presence is derived only from these lock files, never process lists.
  Running work has an animated spinner, and progress bars ease to new totals
  when a journal update lands. On a non-TTY input the keys are disabled and
  selection automatically follows the active task; on non-TTY output colour
  is disabled. `NO_COLOR` also disables ANSI colour, and a non-UTF-8 locale
  uses ASCII borders and status glyphs. Labels default to English and switch
  to Portuguese when `BATUTA_LANG`, `LC_ALL`, or `LANG` starts with `pt`.

  **Colours.** Integrated states are green, running states are blue, blocked
  states are bold red, waiting states are bold yellow, and pending states are
  dim. The selected task uses reverse video, with dim reverse when focus is on
  the log; the focused box has a blue border. Log progress is bold cyan, log
  errors are red, and prompts are bold. `NO_COLOR` disables all ANSI styling.

## Roadmap

A roadmap is the level above the plan: the delivery as a whole, split into
phases, each phase one plan approved on its own. Waves stay computed from
`Depends on`; they are never written.

```markdown
# Roadmap — <title>

- [x] 1. <phase title> → plans/<slug>.md
- [ ] 2. <phase title> → plans/<slug>.md
- [ ] 3. <phase title>
```

Contract (`routing.ParseRoadmap`, file `.batuta/roadmap.md`): line 1 is
`# Roadmap — <title>`; a phase is `- [ ] N. <title>` or `- [x] N. <title>`,
numbers start at 1 and increase by one; the optional tail ` → plans/<slug>.md`
names the plan (`.batuta/plans/<slug>.md`, or `.batuta/plans/done/<slug>.md`
once finished); a phase without a tail is listed but not planned yet.
Everything else is prose. A broken line fails with its number, like a plan.

`batuta loop --roadmap` runs the phases in order: the first phase not ticked
must have a plan with `Status: approved`; the loop opens one delivery for it
on the current branch, runs it to implementation `done`, archives the plan,
and runs full review. Only after the durable review gate clears does it tick
the roadmap line and open the next phase on the head the previous one left.
`review_blocked` stops progression with exit `2` across restarts.
`--dry-run --roadmap` prints the chain (phase, plan, state) and runs nothing.
The chain stops with the delivery's state: `blocked` is terminal (fix the
cause and run the roadmap again — a new delivery for the same phase);
`waiting_input` continues with `batuta loop --resume <delivery> --roadmap`
after `--answer`. When the next phase has no approved plan the run ends with
`waiting_plan`, exit code 4. The `opened` journal record carries `roadmap`,
`phase` and `phase_title`, and `batuta trail` and the dashboard show them.
`.batuta/roadmap.md` is managed state: exempt from the clean-tree preflight
and from the scope check, committed by the loop with the plan bookkeeping.

## Files the loop writes

| Path | Tracked | When |
|---|---|---|
| `.batuta/journal/<delivery>.jsonl` | no (`.git/info/exclude`) | every transition |
| `.batuta/worktrees/<slug>-task-N-e<k>/` | no | per attempt; removed after integration or abort (`--keep-worktrees` keeps them) |
| `.batuta/runs/<date>-<slug>-task-N.md` (+ `-e<k>.brief.md`, `-e<k>.out.log`) | no | per attempt |
| `.batuta/runs/supervision/<delivery>.json` | no (`.git/info/exclude`) | durable foreground observer cursor |
| `.batuta/reviews/supervision/<job-id>/` | no (`.git/info/exclude`) | final review snapshot, reports, job state and progression judgment |
| `.batuta/asks/<slug>-task-N.md` | no | when a task asks; removed by `--answer` |
| `WORK.md`, `.batuta/plans/<slug>.md` | yes | generated from the terminal delivery summary and journal, once at a final state, in one `chore(batuta): <slug> — loop <state>` commit |
| `.batuta/plans/done/<slug>.md` | yes | when all tasks are done; the bookkeeping commit carries the plan move |

Legacy `.batuta/plan-<slug>.md` plans remain readable for one release. The
active path takes precedence when both exist. Unfinished plans stay at their
loaded location; finished plans move to `.batuta/plans/done/` and are excluded
from plan discovery.

## Not in this release

- Delivery token budgeting or aggregation: ACP receipts can retain optional
  provider-reported usage, but missing counters remain unknown and the graph
  does not consume them. CLI executors report tokens only when the adapter
  declares `usage_regex`. The wall budget
  remains `--task-timeout` per session; paired measurement is described in
  [dispatch-measurement.md](dispatch-measurement.md).
- Cross-review with lenses (the skill's `/batuta-review`); the loop runs the
  independent verifier only.
