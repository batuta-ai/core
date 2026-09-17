# Plan — fix integrated supervision review findings

**Goal:** Correct cancellation exit semantics, explicit skills propagation to final review, and arbitrary writer handling before releasing integrated supervision.
**Created:** 2026-09-16 · **Status:** done

## Tasks
- [x] 1. Preserve signal cancellation while exposing independent failures — backend/high
      Scope: cmd/batuta/main.go, cmd/batuta/main_test.go, loop/supervision_run.go, loop/supervision_run_test.go
      Accept: normal supervised new/resume/roadmap execution returns cancellation exit 130 after joining activity, while independent observer/runtime errors are not hidden by a joined context cancellation; an actual CLI cancellation regression distinguishes this from generic exit 1 → go test ./cmd/batuta -run 'Loop|Supervis'; supervision cancellation and cleanup regressions pass → go test -race ./loop -run SupervisionRun
- [x] 2. Carry explicit skills selection into required review — backend/medium
      Depends on: 1
      Scope: cmd/batuta/main.go, cmd/batuta/main_test.go, loop/supervision_review.go, loop/supervision_review_test.go, loop/supervision_run_test.go
      Accept: review receives the resolved absolute skills directory selected by --skills including from another working directory, without requiring caller BATUTA_SKILLS; normal/resume/roadmap and applicable standalone supervision use consistent explicit selection, and tests do not mask the issue by populating global BATUTA_SKILLS → go test ./cmd/batuta -run 'Loop|Supervis'; review subprocess/probe and existing recovery regressions pass → go test ./loop -run SupervisionReview
- [x] 3. Serialize arbitrary writer output without unsafe interface equality — backend/high
      Depends on: 2
      Scope: loop/supervision_run.go, loop/supervision_run_test.go, loop/runner.go
      Accept: initial and continuation supervision accept non-comparable io.Writer implementations including function-valued writerFunc without panic, preserving shared serialization when worker and observer share output and preserving independent streams; focused regressions exercise both equality sites and continuation → go test -race ./loop -run SupervisionRun; integrated supervision and durable gates remain valid → go test -race ./loop -run 'SupervisionGate|Roadmap'; full suite passes → go test -p 1 ./... -timeout=15m; build passes → go build ./...

## Decisions and context

User authorized continuing implementation and necessary corrections before PR/release. Automatic review covered both cohorts and all 12 selected changed files for the follow-up delivery, but returned REWORK: one blocker (signal cancellation exit) and two majors (explicit skills not propagated, non-comparable io.Writer panic). All reported proof commands passed, demonstrating missing regression coverage rather than release readiness. Conductor inspected code and accepts all three findings. Original review artifact remains immutable at .batuta/reviews/supervision/ea786722b23282af20ffa61ca695fcc6faf3752a0f58a5a1c3ca69bace860e39/artifacts/review.md. Do not change report/outcome, claim accepted delivery, weaken gate, reset review budget or alter recorded evidence.

Current branch feat/integrated-supervision at 5c58a9f includes task1 91fe5e4, task2 9942881, task3 aed3589. Preserve approved persistent review blocking, ownership/uncertainty/budget checks, foreground composition and no automatic Git merge or daemon. Final independent review must cover the entire branch from released base 1257ece, including earlier gate and WIP-restored code outside the last automatic review diff. This correction loop is explicitly conductor-authorized, not an automatic inference from findings.

**Task 1.** runSupervised joins ctx.Err with run/observer errors; CLI currently returns that error before loopExit can map StateCanceled. Fix the responsible boundary and prove both cancellation-only behavior and cancellation joined with a distinct real failure. Do not blanket-convert every errors.Is(context.Canceled) to success/cancellation and hide other errors. Tests inject bounded deterministic subprocesses, not real model calls.

**Task 2.** The CLI constructs SupervisionReviewOptions with only Executable. RunSupervisionReview executes review in its immutable snapshot, so ambient skills discovery can select the wrong directory or fail. Preserve a deliberate explicit selected path through the existing Command environment/options contract; avoid process-global environment mutation and command-string interpolation. Existing fixture sets BATUTA_SKILLS and must gain a variant with it unset and only --skills provided. Keep optional library defaults compatible and source snapshots immutable. Do not alter installed/global skills.

**Task 3.** There are unsafe comparisons both config.Output == r.opts.Stdout and execution.Stdout == r.opts.Stdout. Dynamic interface values can be non-comparable; avoid panic in both. Merely refusing equality is insufficient if it loses necessary shared locking for an inherited writer. Prove concurrency behavior with race detection and preserve user-supplied independent streams. No reflection-heavy general abstraction beyond what the current writer contract requires.

Work test-first; expected initial red test is not an unexpected two-failure stop. Stop for repeated unexplained failures or required changes outside Scope. Runtime adapter grants only existing integration scratch in addition to workspace; use temporary GOCACHE for sandbox cache limitations and report uncertainty honestly. No dependencies, release tooling changes, global install, unrelated cleanup or skipped tests.
