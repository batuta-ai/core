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

## After the run

When the delivery reaches `done`, run
`batuta review --spec .batuta/plans/done/<slug>.md`. Resolve any review verdict
that is not `SHIP`, then open the pull request and attach or link the review
artefacts. The review is the delivery-level gate between the completed loop and
the PR; see [review.md](review.md) for its contract.

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
- **Decisions may be task-scoped.** In `## Decisions and context`, a paragraph
  beginning with `**Task N.**` belongs only to task N; `**Tasks N–M.**` (also
  `N-M`, comma lists, and combinations) belongs to every named task. Unlabelled
  paragraphs are shared. Each executor brief keeps shared and its own marked
  paragraphs in plan order and drops paragraphs marked for other tasks.
- **Routing comes from the table.** A plan's `→ executor/model` hint is
  reported in `--dry-run` when it disagrees with the table and otherwise
  ignored: the user's table is the routing decision (core #18, task
  overrides). `reasoning` follows the lane (`low|medium|high|xhigh`).
- **Usage-limit fallback.** `--max-limit-waits` (default 20) bounds the waits
  in one attempt. At that cap, or when a named reset is more than
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
  on the base per gates 2 and 3 → the task is *already satisfied*: no
  candidate, no commit, ticked in the plan at the end. Unchanged and the
  criteria do not hold → failure (`no_changes`).
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
  cleanup may remove it only after that snapshot succeeds. Final bookkeeping
  deletes only parked refs whose complete tree is already present in branch
  history and reports every remaining recovery ref in the terminal summary.
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
on the current branch, runs it to `done`, archives the plan, ticks the
roadmap line, and opens the next phase on the head the previous one left.
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
