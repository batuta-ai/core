# Plan — preserve completed bound-answer replay

**Goal:** Restore idempotent replay of an already completed bound answer without weakening incomplete-answer recovery.
**Created:** 2026-09-15 · **Status:** done

## Tasks
- [x] 1. Preserve completed answers and strengthen recovery fixtures — backend/high
      Scope: loop/supervision_policy.go, loop/supervision_policy_test.go, cmd/batuta/main_test.go
      Accept: a previously answered decision remains unchanged when replayed with different MaxAttempts, without rewriting its ledger, while incomplete matching durable answers still recover → go test -race ./loop -run SupervisionPolicy; mismatch fixtures include an unchanged positive control and assert distinct rejection reasons without resetting budgets → go test ./loop -run SupervisionPolicy; CLI continuation fixture remains independent of git being installed only in system directories → go test ./cmd/batuta -run Supervision; full suite passes → go test -p 1 ./... -timeout=15m; module builds → go build ./...

## Decisions and context

User authorized ongoing corrections and release preparation. Base 424455b on draft PR #86, feat/release-integration. Fable review covered all 3 cohorts and found ordering regression in InterveneSupervision: bound-answer recovery runs before already-answered fast path, so a different policy digest changes replay behavior and identical replay rewrites ledger. Restore terminal answered semantics while retaining validated recovery of incomplete answer attempts. A completed existing decision does not authorize another execution or scope change. Preserve policy identity checks on incomplete attempts, exact bound answer validation, budgets and source evidence. Add actual regression before fix.

Related review feedback: foreign-record mismatch tests need an unchanged positive control and specific reasons to avoid vacuous passes. CLI fixture replaces PATH with hardcoded system directories, so preserve lookup of real git when it lives elsewhere while retaining owned fake executor isolation. Do not rewrite unrelated tests or add production test flags.

Go standard library, TDD, conventional commit, task worktree only. No main merge, release, global installation, new dependency, provider changes or rewriting historical journal data. Ignore optional wrapping/parallelization nits. Routine syntax fixes and expected red tests are authorized. Known worker sandbox cache/preflight limitations are environmental: use writable temporary GOCACHE and report. Stop for actual scope/authority conflicts or two attempted fixes failing for the same unexpected cause. After verification the conductor updates PR #86 and obtains review of the final patch.
