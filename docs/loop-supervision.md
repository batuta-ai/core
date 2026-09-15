# Foreground loop supervision

`batuta loop --supervise` watches one explicit delivery from its append-only
journal. It is a local foreground process: it does not install a service, start
a daemon, or take ownership merely to observe. Observation itself makes no model
calls. Automatic review is opt-in: it runs only in this `--supervise` process,
after the delivery is fully finalized, and does not require `--policy`.

```bash
batuta loop --workspace /absolute/repository \
  --supervise <delivery> \
  --cursor /absolute/private-state/<delivery>.json \
  --interval 500ms \
  --notify /absolute/existing-notification-directory
```

The interval must be between `100ms` and `1m`. Use `--once` for a single
observation in smoke tests. The cursor must be an absolute path outside the
delivery journal directory and should be private to the intended notification
consumer. Reusing it after a supervisor restart resumes from the recorded
journal sequence and preserves unread events.

## Lifecycle and durable delivery

The journal, not process presence, establishes delivery state. A fresh lock can
report `running`; a stale or missing lock reports only process presence and
never turns an executor exit into completion. `waiting_input`, `blocked`, and
`canceled` remain terminal observations that need operator attention. Only a
`done` terminal record with no pending cleanup, bookkeeping, or recovery-ref
deletion is complete. For legacy deletion intent, the observer checks the exact
recorded Git refs: only confirmed absence clears that intent. Lookup errors,
dangling symbolic refs and malformed intent remain pending; cleanup and
bookkeeping flags are never inferred away. The terminal event keeps its ID and
acknowledgment, independently of review events at the same journal sequence.
An answer followed by resumed journal activity supersedes
the earlier `waiting_input` state.

Actionable journal records become redacted events of at most 4 KiB. Each event
has a stable `<delivery>:<journal-sequence>` ID and a digest-bearing reference
to the full journal record; raw questions, logs, errors, and artifact paths are
not copied into the event. The durable cursor records both progress and an
outbox. Multiple or restarted observers using the same cursor serialize access,
so they do not create duplicate outbox entries.

Notification delivery is at least once across crashes. An event is acknowledged
only after the sink returns success. A crash after the sink accepts an event but
before cursor acknowledgment can repeat it, so consumers must deduplicate by
event ID. With no `--notify`, events remain unread in the cursor.

Supervisor persistence validates the opened file before reading, with bounded
reads. Unix uses nonblocking opens to reject FIFOs, including symlink targets
and path replacements, without waiting for a writer. Writes flush a private
temporary file before atomic replacement; Unix also flushes the directory,
while Windows requests write-through replacement. Native Windows runtime
qualification remains unavailable on this macOS host; cross-compilation alone
does not prove Windows runtime behavior.

## Notification prerequisites and host limits

`--notify <absolute-directory>` writes one private JSON file per event into an
existing local directory. The host must keep the foreground supervisor running
and arrange to consume that directory; writing a file does not wake a chat.

`--notify desktop` uses an already-installed local notification facility:
`osascript` on macOS or `notify-send` on Linux. It installs nothing, is
unsupported on other operating systems, and acceptance by the local service
does not prove that a person read the notification. A missing command, denied
desktop session, or failed sink leaves the event unread for retry.

There is no Slack, email, remote message, or spontaneous ChatGPT/Codex delivery.
If the host cannot keep a process alive or receive asynchronous notifications,
run `--once` when convenient and inspect the durable cursor/output; do not treat
ending a chat turn as supervision.

## Intervention policy

Without `--policy`, observation and automatic review still run, but worker
questions and review findings remain pending for conductor judgment. A policy
supports two actions only: `continue_approved_task` provides one fixed routine
clarification for a worker question, while `propose_correction` reserves a
correction delivery after an operator has judged a completed review.

For `continue_approved_task`, the JSON must bind the delivery, task, execution,
question ID and question journal digest. It must also bind a repository-local
plan path and SHA-256 digest, attest `"ownership":"approved_task"`, and set
`max_attempts` from 1 through 3. This action only says to continue the task
already assigned by the approved plan; it adds no scope or permission.

The delivery-wide decision ledger survives supervisor restarts and policy-file
changes. An attempt is recorded before the existing bound-answer API is called.
Mismatched evidence, unresolved ownership, a question that is no longer
pending, or uncertain ACP execution remains pending for reconciliation. Worker
prose and error classification never grant authority. The supervisor does not
treat model output as executable policy and does not invoke a conductor model.

### Authorized runner continuation

With `continue_approved_task`, the CLI resumes the normal runner after the bound
answer. It accepts `--skills`, `--transport`, `--parallel`, `--task-timeout`,
`--test-timeout`, `--max-waves`, `--keep-worktrees`, `--max-limit-waits`,
`--limit-wait`, and `--limit-horizon` alongside the observer flags. These runner
settings require `--policy`; routing, gates, retry budgets and usage limits
remain the normal runner's responsibility. Worker output goes to stderr so
supervisor stdout remains JSON. Embedders explicitly supply `Execution` to
`Supervise`; `InterveneSupervision` alone still only submits the answer.

A delivery-wide continuation intent binds the event, policy, execution settings,
bound answer and subsequent dispatch evidence. A guard spans resume and run.
Concurrent observers or restarts cannot launch another runner for the same
consumed continuation. Durable intent alone supplies no authorization: missing
policy, changed settings or plan evidence, live/stale/unreadable ownership,
uncertain submission and conflicting journal activity require reconciliation.
An interrupted answer attempt can be acknowledged from its exact durable bound
answer without resetting its attempt budget or submitting another answer.

If the resumed delivery completes, the same supervisor runs automatic review,
including in `--once` mode. Review launch intent and immutable receipts still
prevent automatic replay. A `propose_correction` policy only reserves the child
identity; it never enables this continuation or requires worker execution
settings. Stale review evidence cannot invalidate a current valid proposal.

## Cost accounting

Passive observation makes zero model calls. Report its costs separately as
poll count, journal/cursor bytes read and written, notification attempts,
elapsed time, and host resource measurements when available. These are
observation costs, not tokens.

Intervention usage is a separate nullable measurement. The fixed policy path
itself uses no model; any worker session resumed after the bound answer belongs
to normal worker usage. If a future explicitly configured conductor model is
measured, record its provider-reported input, cached input, output, and
reasoning fields with provenance under intervention, never under observation.
Missing counters are `unknown`, not zero. Do not claim percentage savings
without a matched baseline measured under the protocol in
[dispatch-measurement.md](dispatch-measurement.md).

## Review outcomes and correction proposals

After a fully finalized `done` record, supervision reviews the recorded final
commit against the exact original base and delivered spec. It probes the current
binary and invokes `review --base ... --spec ... --full --out ...` in an isolated
snapshot. A later delivery cannot move the reviewed source or reuse incremental
coverage. Legacy journals with unknown final identity remain visibly pending.
Missing capabilities, unresolved snapshots and execution failures never become
successful reviews; nothing is installed as a fallback.

The job record is `.batuta/reviews/supervision/<job-id>/job.json`. The immutable
spec copy is `.batuta/reviews/supervision/<job-id>/<slug>.md`, and the isolated
source snapshot is the sibling `source/` directory. Only the engine's
digest-bound `manifest.json`, `findings.json`, `review.md`, and `state.json` live
in the `artifacts/` subdirectory beneath that job directory.
Execution state (`pending`, `launching`, `reported`, `failed`, `uncertain`) is
separate from outcome (`SHIP`, `FIX_BEFORE_SHIP`, `REWORK`,
`incomplete_coverage`, `execution_failed`). The engine's exit status, canonical
walkthrough, and coverage checkpoint must agree. Recovery from a durable
`launching` state records `uncertain` without necessarily assigning an outcome;
it remains uncertain until execution is reconciled, and observing it does not
authorize replay.
There is one logical job per immutable delivery identity, with durable launch
intent, not a claim of exactly-once external execution across crashes.

The review API defaults to one hour, and the supervision CLI exposes no review
timeout flag, so the CLI timeout is fixed at one hour. Cancellation or timeout
while acquiring review ownership returns an error before any job transition and
does not disturb the current owner's job. Once ownership is acquired, an
interrupted capability probe or snapshot preparation is recorded as `failed`
with outcome `execution_failed`. After the durable `launching` transition and
attempt increment, cancellation or timeout while the engine command is running,
or unresolved descendant cleanup, is recorded as `uncertain` with outcome
`cleanup_unresolved`. Once the engine exits, snapshot, delivery-identity,
artifact, or classification verification can instead record `failed` with
outcome `execution_failed`.

The supervisor does not automatically replay an uncertain launched attempt.
Nor does it claim that reviewer descendants were terminated on hosts where that
cannot be verified. The operator must reconcile the reviewer process and
retained evidence before any further attempt.

`completed` describes implementation and finalization only. `acceptance` remains
`pending`, including after SHIP: the report is evidence for conductor judgment,
not self-approval or permission to merge, release, publish, or install globally.
Review transitions enter the existing cursor outbox with stable IDs, status and
a digest-bearing immutable `outcomes/<digest>/job.json` reference. File and desktop sinks retain the same
acknowledgment and retry behavior as delivery events. Raw findings remain in the
artifacts. No notification wakes a chat by itself.

An operator can use the existing `--policy` dispatch to reserve a correction
proposal for a completed FIX_BEFORE_SHIP or REWORK review:

```json
{
  "delivery": "original-delivery",
  "action": "propose_correction",
  "ownership": "approved_correction",
  "plan_evidence": {
    "path": ".batuta/reviews/supervision/<review-job-id>/<slug>.md",
    "digest": "sha256:<SHA-256 of the operator-supplied plan bytes at that path>"
  },
  "max_attempts": 3,
  "correction": {
    "review_id": "<review job ID>",
    "report_digest": "sha256:<review event evidence digest>",
    "spec_digest": "<original delivered contract digest>",
    "delivery": "correction-one",
    "max_corrections": 2
  }
}
```

The operator must first judge the findings and explicitly authorize in-scope
corrections. `plan_evidence.digest` authenticates the exact operator-supplied
bytes at `plan_evidence.path`; it does not assert that those bytes equal the
reviewed spec. Independently, the supervisor byte-authenticates the immutable
reviewed spec saved as `<slug>.md`. It parses both documents and requires an
equivalent task digest, title, goal, and effective shared/task context. The plan
header's `**Status:**` metadata may differ because it does not change that
contract. Task checkbox state is different: pending versus completed is part of
the task-set digest, so a copy with ticked tasks is not equivalent to the
original task state. In particular, `plan_evidence.path` need not be
`.batuta/plans/done/<slug>.md`, and a completed archive whose task checkboxes were
ticked will not match the original task-set digest. Review prose cannot supply
policy, extend scope, resolve ownership, or authorize uncertain execution.
Incomplete coverage and execution failures require a decision rather than a
correction proposal.

The workspace-wide `.batuta/journal/supervision-corrections.json` ledger reserves
child delivery identities and retains chain budgets across cursors, restarts,
and policy changes. Both bounds are 1–3. For correction proposals,
`max_attempts` is chain-wide and counts each recorded local proposal attempt,
including one that later fails plan or ownership validation;
`max_corrections` caps correction depth. A budget can shrink but cannot be
increased by replacing a policy. Exhaustion is explicit. Repeating an accepted
proposal returns its original child identity without creating another
correction. The worker-answer ledger is delivery-wide; its `max_attempts`
counts recorded bound-answer submissions, including a rejected submission, but
pre-submission evidence and ownership checks do not spend it.

A proposal does not create a plan, invoke a conductor model, answer a worker
question, resume a runner, or change the completed journal. The conductor must
create the proposed child delivery using the same approved contract, with its
opening base equal to the reviewed final commit. Supervise that explicit child
ID after completion: its new recorded final commit gets a distinct full review,
and any further proposal inherits the original chain budget. A changed contract
or an unrelated base requires a separate scope decision. Existing worker retry
and usage quotas remain in force. The local proposal policy makes zero model
calls; provider token usage for actual review/worker sessions is separate and
must not be inferred from these attempt counters.
