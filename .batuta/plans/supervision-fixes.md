# Plan — make supervision complete real lifecycle transitions

**Goal:** Correct the confirmed real-journal completion error and make an authorized routine answer actually resume the existing runner without duplicate or uncertain execution.
**Created:** 2026-09-12 · **Status:** approved

## Tasks
- [ ] 1. Reconcile terminal deletion intent against exact existing refs — backend/high
      Scope: loop/supervision.go, loop/supervision_test.go, loop/supervision_notify_test.go
      Accept: done with historical deletion intent and all exact refs absent becomes completed, while any present ref including a replaced SHA, lookup failure, cleanup or bookkeeping flags remains unresolved → go test ./loop -run Supervision; restarted cursors reconcile old terminal outbox state without conflicting completion notifications, observation never deletes refs or rewrites the journal → go test -race ./loop -run Supervision
- [ ] 2. Resume the runner after a policy-authorized bound answer — backend/high
      Depends on: 1
      Scope: loop/supervision.go, loop/supervision_test.go, loop/supervision_policy.go, loop/supervision_policy_test.go, cmd/batuta/main.go, cmd/batuta/main_test.go, docs/loop-supervision.md
      Accept: configured policy answer leads through normal Resume and Run to new delivery activity with explicit execution settings, absent policy remains observation-only → go test ./cmd/batuta ./loop -run Supervision; separate durable continuation intent survives crashes after answer and before/after ownership acquisition without concurrent duplicate runners, uncertain work remains blocked → go test -race ./loop -run Supervision; full suite and build pass → go test ./...; module builds → go build ./...

## Decisions and context

User prioritized urgent autonomous supervision and approved continuing improvements. These are confirmed completion gaps, not new unrelated features. Base is supervisor252ff12; its full review is currently running and must finish before changing that reviewed source. Integrate with the separate deep-review layer only after independent verification. Use installed existing primitives; no daemon, dependencies, global install, remote notification or privilege change.

Real evidence: ACP correction delivery acp-review-fixes-20260911-205553 final record63 says done, false cleanup/bookkeeping flags and three pending_ref_deletions. Exact refs are absent. Older finalizer records terminal before deletion and appends no follow-up; current observer treats the durable deletion intent as remaining work forever. Resolve only exact recorded refs with bounded read-only lookup. Absence resolves deletion-only ambiguity; any present/ref replacement or lookup error preserves uncertainty. Do not loosen explicit recovery flags.

InterveneSupervision calls answerDelivery, which records answer then releases ownership. It does not call Resume/Run; the current CLI --answer path does both. Reuse lifecycle with explicit routed options, durable continuation intent and ownership reconciliation, not a second scheduler or arbitrary shell execution. Answered is not resumed; record both stages honestly. Never infer authorization from worker prose, missing process or elapsed time. Known sandbox cache/preflight limitations remain environment failures. Expected red regression tests are progress; stop only for the same unexpected cause after two failed fixes, scope conflict or missing authority.
