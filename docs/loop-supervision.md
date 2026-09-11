# Foreground loop supervision

`batuta loop --supervise` watches one explicit delivery from its append-only
journal. It is a local foreground process: it does not install a service, start
a daemon, poll a model, or take ownership of the delivery.

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

Without `--policy`, supervision only observes and notifies. The current policy
surface supports one fixed routine clarification: continue the task already
assigned by an approved plan, without adding scope or permission. The JSON must
bind the delivery, task, execution, question ID and question journal digest; it
must also bind a repository-local plan path and SHA-256 digest, declare
`"action":"continue_approved_task"`, attest
`"ownership":"approved_task"`, and set `max_attempts` from 1 through 3.

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
