# Plan — economical loop supervision

**Goal:** Supervise a delivery without repeated model polling, resolve routine pauses within existing authorization, and surface completion or decisions through an available notification channel. User approved this direction on 2026-09-11; implementation breakdown authorized with the subsequent request to proceed with review and remaining improvements.
**Created:** 2026-09-11 · **Status:** approved

## Tasks
- [ ] 1. Observe delivery events with durable deduplication — backend/high
      Scope: loop/supervision.go, loop/supervision_test.go
      Accept: journal observations emit compact actionable events once per persisted delivery/sequence identity, survive restart and tolerate partial trailing records → go test ./loop -run Supervision; terminal state is distinguished from process exit, stale activity never proves failure, observation makes zero model calls → go test ./loop -run Supervision
- [ ] 2. Bound intervention and bind answers to their question — backend/high
      Depends on: 1
      Scope: loop/supervision.go, loop/supervision_test.go, loop/supervision_policy.go, loop/supervision_policy_test.go
      Accept: routine interventions require explicit scoped policy and matching pending question identity, with decision evidence and bounded retries → go test ./loop -run Supervision; permission grants, scope expansion, quota changes and uncertain execution replay are never inferred from worker prose or silence → go test ./loop -run Supervision; absent policy or unresolved ownership leaves the question pending, concurrent observers cannot answer twice → go test -race ./loop -run Supervision
- [ ] 3. Expose opt-in local supervision and actionable notifications — backend/high
      Depends on: 2
      Scope: cmd/batuta/main.go, cmd/batuta/main_test.go, loop/supervision.go, loop/supervision_test.go, loop/supervision_notify.go, loop/supervision_notify_test.go
      Accept: an explicit delivery can be supervised with bounded resources, graceful cancellation and durable event output, while legacy loop invocation remains unchanged → go test ./cmd/batuta ./loop; completion and pending decisions reach a configured local sink with stable event IDs and explicit delivery state, missing or failed notification delivery remains visible and never claims a chat notification → go test ./loop -run Supervision
- [ ] 4. Verify lifecycle and measure supervision overhead — docs/medium
      Depends on: 3
      Scope: docs/loop-supervision.md, docs/loop.md, docs/dispatch-measurement.md, README.md, README.pt-BR.md, loop/supervision_test.go
      Accept: deterministic scenarios cover running, waiting_input, done, blocked, canceled, restart and duplicate observers → go test -race ./loop -run Supervision; full regression suite passes → go test ./...; documentation separates observation cost from intervention tokens and states notification prerequisites and remaining host limits

## Decisions and context

Priority changed explicitly by user: implement supervision urgently in an isolated delivery based on ACP commit231cd3f while the independent ACP correction loop completes. Integrate branches only after both are stable and reviewed; do not alter the active ACP delivery. Do not change the active acp-dispatch plan, its task graph or expected HEAD. This is a separate approved product direction with an approved implementation breakdown, not supervision already installed or running.

Reuse the journal, delivery ownership, bound-answer API and existing lifecycle primitives. Start with one foreground local supervisor attached to an explicit delivery and a durable cursor. No new daemon, scheduler, distributed queue or dashboard. Observation can use bounded file polling if native events add complexity, but it must not poll an LLM. A running foreground process can be launched by an existing host facility; ending a chat turn alone provides no wake-up guarantee.

Default intervention is notification only. A supplied policy may authorize a known routine response, such as clarifying ownership already explicit in the approved plan. Novel diagnosis may invoke one explicitly configured conductor executor with a compact event, relevant plan excerpt and artifact references, under a fixed budget and a per-question attempt limit. Treat its output as a proposal: validate identity, scope and policy before the existing answer API accepts it. An error classification or model claim alone is never permission. Uncertain ACP execution must retain reconciliation requirements.

Use at most a 4 KiB actionable event with overflow references. Keep raw logs outside conductor context and redact sensitive material. Record nullable measured model usage separately from observation, retries and elapsed time. Zero model calls while simply waiting is an acceptance condition; no percentage savings claim without a matched measurement.

Deliver notifications through an explicitly configured local sink first; external messaging requires a separately authorized destination. If the host cannot receive asynchronous notifications, document that limitation and leave a durable unread event rather than claiming spontaneous chat delivery. No automatic install or global configuration changes. Test with fake journals, a fake clock and fake executor/sink, without paid model calls or real external messages.

Urgency means reuse existing primitives and deliver the four scoped tasks now, not create an extra orchestration framework. Interface should expose explicit delivery, bounded poll interval, once mode for smoke tests, local notification sink and optional intervention policy. Notification delivery cannot promise exactly once across crashes without an idempotent sink: persist an outbox/event ID, distinguish pending/acknowledged, and document any at-least-once window. Durable event recording and repeated observation must deduplicate deterministically. A native local desktop notification is in scope where supported, invoked without shell interpolation; no Slack/email/remote messages or background service installation.

Optional conductor invocation must be explicit, routed to an exact executor/model/effort, bounded by time/output/attempts, and receive only compact event plus necessary plan excerpt and evidence references. An allowed routine continuation may use a fixed, scoped answer preserving original plan boundaries and tests. Arbitrary model prose must not become shell commands, permission grants, scope changes or replay authorization. Keep absent policy notification-only. The current two-failure stops often concern incorrectly authored test expectations; distinguish same-cause repeated failed fixes from expected red tests or a different newly exposed cause.

Known worker sandbox limitation: integration scratch and Go cache can be outside writable workspace. Conductor baseline tests pass outside sandbox. Use temporary writable GOCACHE for local checks, report sandbox-only full-suite failures and leave integration/cache production unchanged. A newly introduced failing regression is expected red; stop only when the same unexpected cause persists after two attempted fixes, not when the command has any two failures. Independent gates remain required.
