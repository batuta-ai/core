# Plan — correct ACP review findings

**Goal:** Resolve the accepted first-review defects while preserving default CLI behavior, bounded protocol resources and uncertain-work reconciliation.
**Created:** 2026-09-11 · **Status:** approved

## Tasks
- [ ] 1. Preserve task timeout classification across transports — backend/medium
      Scope: executor/transport.go, executor/transport_test.go, executor/dispatch.go, executor/dispatch_test.go, executor/acp_backend_test.go
      Accept: task-owned deadlines produce TimedOut and dispatch timeout exit 124 consistently for explicit CLI, auto fallback and ACP, while caller cancellation remains distinct → go test ./executor/...; existing fallback and no-replay cases pass → go test ./executor/...
- [ ] 2. Separate prompt lifetime and inbound response capacity — backend/high
      Depends on: 1
      Scope: executor/acp/connection.go, executor/acp/connection_test.go, executor/acp/session.go, executor/acp/session_test.go, executor/acp/permission_test.go, executor/acp/review_ordering_test.go, executor/acp_backend_test.go
      Accept: prompt can outlast the control request timeout within its finite task budget, while missing control replies and blocked writes remain bounded → go test -race ./executor/...; with MaxPending 1, a pending prompt can receive an explicit permission decision and cancellation without ErrCapacity, and queue limits remain enforced → go test -race ./executor/...; earlier configuration ordering regressions remain green → go test ./executor/acp
- [ ] 3. Preserve independent verifier submission uncertainty — backend/high
      Depends on: 2
      Scope: loop/attempt.go, loop/attempt_test.go, loop/runner.go, loop/settle.go, loop/settle_test.go, loop/report.go, loop/report_test.go, loop/loop_test.go
      Accept: a CLI task with an injected ACP verifier retains separate verifier identity and submission evidence, disconnect or unresolved shutdown blocks replay and preserves worktree → go test ./loop; crash during verifier execution is reconciled without losing CLI task identity or replaying uncertain work, old journals and stock CLI verifier remain compatible → go test ./loop; full suite and build pass → go test ./...; module builds → go build ./...

## Decisions and context

User authorized review followed by corrections and remaining improvements. Execute only after isolated acp-ordering-fix is verified and integrated; that separate correction resolves session.go:151 and must not regress. Evidence/judgments: .batuta/runs/acp-review-judgment.md and .batuta/reviews/acp-dispatch-full/. Full engine review covered all files and passed existing proofs; tests missed these behavior defects. No release, global install, new dependencies, production qualification or default transport change. Keep generic two-failure stop limited to the same unexpected cause after two attempted fixes; expected red tests or newly exposed causes are progress. Sandbox cache/preflight limitations already verified outside sandbox must be reported, not repaired in production.

**Task 1.** transport.go owns the outer deadline for acp/auto, so its downstream backend cannot distinguish that task timeout from caller cancellation. dispatch.go finding shares this cause. Verify all return paths including pre-prompt CLI fallback. Do not infer that a timed-out submitted task is safe to replay.

**Task 2.** Connection.Call currently applies RequestTimeout to session/prompt and holds an outbound slot until completion. Respond/Notify share those slots, starving required permission replies/cancel when capacity is full. Separate these bounds without removing resource limits. Test behavior using controlled peers and deterministic synchronization; no sleeps as fixes, no paid model calls. An unconfirmed cancellation-before-pending-prompt race was suggested; investigate if touched, never claim confirmed without proof.

**Task 3.** verify currently discards receipt/identity into gates.Verdict; failure policy sees only original task result, potentially CLI. Verifier execution evidence must survive journal crash boundaries independently of task identity and propagate to reconciliation. Readonly invocation is not proof a remote process exited. Stock verifier remains CLI; custom ACP verifier uncertainty must not cause retries or cleanup. Do not change legacy CLI limit policy or unrelated report behavior.
