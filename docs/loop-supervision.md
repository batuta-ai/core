# Foreground loop supervision

`batuta loop --supervise` watches one explicit delivery from its append-only
journal. It is a local foreground process: it does not install a service, start
a daemon, or take ownership of the delivery. Observation itself makes no model
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
deletion is complete. An answer followed by resumed journal activity supersedes
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
execute model output or shell commands and does not invoke a conductor model.

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
spec copy is `.batuta/reviews/supervision/<job-id>/<slug>.md`; digest-bound
`manifest.json`, `findings.json`, `review.md`, and `state.json` live in the
`artifacts/` subdirectory beneath that job directory.
Execution state (`pending`, `launching`, `reported`, `failed`, `uncertain`) is
separate from outcome (`SHIP`, `FIX_BEFORE_SHIP`, `REWORK`,
`incomplete_coverage`, `execution_failed`). The engine's exit status, canonical
walkthrough, and coverage checkpoint must agree. An interrupted launch remains
uncertain until execution is reconciled; observing it does not authorize replay.
There is one logical job per immutable delivery identity, with durable launch
intent, not a claim of exactly-once external execution across crashes.

The review's default and CLI-fixed timeout is one hour; there is no supervision
timeout CLI flag. A timeout, cancellation, or unresolved
review-descendant cleanup leaves the job `uncertain` with outcome
`cleanup_unresolved`. The supervisor will not replay that attempt or declare
cleanup complete: the operator must reconcile the reviewer process and retained
evidence before any further attempt.

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
    "path": ".batuta/reviews/supervision/<review-job-id>/delivery.md",
    "digest": "sha256:<SHA-256 of those exact original approved plan bytes>"
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
corrections. `plan_evidence` names the immutable original approved plan copy,
not `.batuta/plans/done/<slug>.md`: final bookkeeping ticks and archives that
copy, so its byte digest differs even though historical parsed task digests stay
compatible. The exact plan bytes, title, goal, task context, and parsed contract
must match the reviewed evidence. Review prose cannot supply policy, extend
scope, resolve ownership, or authorize uncertain execution. Incomplete coverage
and execution failures require a decision rather than a correction proposal.

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
