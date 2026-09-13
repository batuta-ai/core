# Plan — resolve adjudicated automatic review defects

**Goal:** Correct confirmed automatic review delivery defects while preserving immutable evidence, bounded authorization and process ownership.
**Created:** 2026-09-13 · **Status:** done

## Tasks
- [x] 1. Recover final bookkeeping identity despite commit trailers — backend/high
      Scope: loop/report.go, loop/report_test.go, loop/loop_test.go
      Accept: a message-altering commit hook does not strand done finalization and recovery resolves the original unique final commit after newer commits → go test ./loop -run 'Supervision.*Identity'; unknown and ambiguous identities remain explicit and no moving HEAD fallback is introduced → go test ./loop -run 'Supervision.*Identity'; module builds → go build ./...
- [x] 2. Bind correction proposals to full approved contract and durable review evidence — backend/high
      Depends on: 1
      Scope: loop/supervision_policy.go, loop/supervision_policy_test.go, loop/supervision.go, loop/supervision_test.go, loop/supervision_review.go, loop/supervision_review_test.go
      Accept: valid plan bytes with altered goal or effective context cannot pass the unchanged task digest guard and extra task changes are detected with specific reasons → go test ./loop -run SupervisionCorrection; a policy pinned to a real older review receipt stays pending on evidence drift instead of crashing supervision, while fabricated events and stale evidence never authorize corrections → go test -race ./loop -run Supervision
- [x] 3. Make review ownership acquisition cancelable — backend/high
      Depends on: 2
      Scope: loop/supervision_review.go, loop/supervision_review_test.go, loop/presence.go, loop/flock_unix.go, loop/flock_windows.go, loop/flock_other.go, loop/supervision_lock*.go
      Accept: a canceled second supervisor cannot remain indefinitely blocked behind an active review and no concurrent duplicate engine starts → go test -race ./loop -run SupervisionReview; existing presence and recovery invariants remain intact → go test ./loop -run 'Presence|Supervision'; module builds for supported targets without introducing bare cross-platform syscalls → go build ./...
- [x] 4. Bound cancellation and reconcile nested review process cleanup — backend/high
      Depends on: 3
      Scope: loop/supervision_review.go, loop/supervision_review_test.go, loop/supervision_process*.go, publication/command*.go, cmd/batuta/main.go, cmd/batuta/main_test.go
      Accept: canceling review terminates owned nested reviewer groups in a controlled real-process fixture with bounded escalation, or records unresolved cleanup explicitly without permitting replay or acceptance → go test ./loop ./publication -run 'Supervision|Cancel|Process'; cancellation is bounded and ownership is retained while descendant cleanup remains unresolved → go test -race ./loop ./publication -run 'Supervision|Cancel|Process'; module builds → go build ./...
- [x] 5. Prove CLI review wiring and production failure outcomes — backend/medium
      Depends on: 4
      Scope: cmd/batuta/main_test.go, loop/supervision_review_test.go, loop/supervision_policy_test.go, loop/report_test.go
      Accept: a behavior test actually traverses loop supervise review wiring and fails if automatic review is omitted → go test ./cmd/batuta -run Supervision; review exit statuses 2 and 3 exercise real wrapped ExitError semantics and missing full flag tests discriminate that flag → go test ./loop -run SupervisionReview; controlled cancellation exercises context propagation and goroutine helpers avoid FailNow outside the test goroutine → go test -race ./loop -run SupervisionReview
- [x] 6. Align operator documentation with verified review behavior — backend/medium
      Depends on: 5
      Scope: cmd/batuta/main.go, docs/loop-supervision.md, docs/loop.md, README.md, README.pt-BR.md
      Accept: help documents both policy actions and review without policy, examples use original immutable plan evidence and correct artifact paths, fixed timeout and unresolved cleanup limitations are explicit → go test ./cmd/batuta -run Supervision; full regression suite passes → go test ./...; module builds → go build ./...

## Decisions and context

User approved correcting the adjudicated findings after reviewing Fable 5.1 and Grok 4.6 reports. This plan operationalizes that approval. Base 193ec4d is the completed automatic review branch; other supervisor persistence/resume fixes live separately and must not be silently merged or reimplemented here. Work only on this delivery and task Scope. No merge to main, release, global installation, provider/model routing changes, new dependencies or remote notifications. Preserve historical journal/task digest compatibility and frozen exported APIs. Go standard library, TDD, conventional commits, one verified task per commit. Independent root verification passed go test ./... (loop about 167 seconds), go build ./..., and race Supervision checks at base.

Adjudicated report is in the source delivery .batuta/reviews/cross-review-judgment.md and copied here under .batuta/runs/cross-review-judgment.md for reference. Prior reviews have incomplete independent coverage; their absence of findings is not approval. Every task needs its own discriminating regressions. Use expected red tests as progress, not a stop. Known worker sandbox cache/preflight restrictions are environmental: use writable temporary GOCACHE and report limitations; conductor gates run outside sandbox. Stop only after two attempted fixes fail for the same unexpected cause, unresolved authority or a true Scope conflict. Do not solve another task ahead of its turn.

**Task 1.** report.go matches the entire regenerated message against git log %B. A hook adding a trailer retains Delivery ID but breaks equality. Preserve uniqueness and recovery safety: no broad subject-only identity, no moving HEAD, no trust in arbitrary matching commits. Prefer existing commit identity mechanisms where available and preserve old journals. Fold duplicated message formatting and focused later-HEAD test correction into this fix only if needed.

**Task 2.** parsed.Set.Digest excludes Plan.Goal and effective ContextFor(task), which loop.Brief executes. Compare complete instructions with immutable reviewed spec without migrating historical digests. Changed plan test must recompute the evidence byte digest to reach the actual contract guard. Old legitimate review events can remain in durable outbox after job evidence changes; return a durable pending evidence-mismatch decision rather than unknown-event error. Authenticate old receipt identity, not merely a string prefix. Current attempt accounting intentionally charges failed local checks and is documented/tested: preserve it. Do not silently ignore stale locks, symlinks or ambiguous ownership or narrow workspace exclusion to one delivery. Ownership-policy redesign and budget semantics are deferred.

**Task 3.** guardPresence uses blocking exclusive OS locking before the review timeout is established and holds ownership through engine execution. Introduce narrowly scoped cancelable/bounded review acquisition without weakening other presence operations. Do not unlink shared guard inodes. Maintain platform build compatibility; cross-compilation is not Windows runtime qualification.

**Task 4.** publication.ExecRunner creates separate process groups for the outer engine and inner reviewer. Killing only the outer group with SIGKILL does not prove descendants stopped. Reuse existing cancellation/ownership infrastructure with graceful bounded escalation or verified containment. Keep process identity safe against PID reuse. A still-uncertain descendant must keep the job uncertain, prevent relaunch and acceptance, and expose that state. Test real nested helper processes without provider calls. Do not broaden all subprocess behavior unnecessarily; preserve existing caller semantics. Do not claim native Windows cleanup verified on this macOS host.

**Task 5.** Current CLI test checks review help and capabilities only. Use a real command boundary or existing injectable seam to exercise supervise behavior without production test flags. Current outcome fake sets nonzero result but returns nil error, missing the actual ExitError branch. Use matching real statuses, not an unrelated git error. Missing flag help currently fails base formatting rather than missing full. Existing canceled fake does not cancel its context. Keep tests targeted, avoid blanket coverage churn, blanket parallelization or timing-sensitive sleeps.

**Task 6.** Document that automatic review requires --supervise and runs without --policy; policy supports scoped answers and correction proposals. Explain the original approved plan bytes versus ticked archive digest, artifacts subdirectory, one-hour default if unchanged, and recovery limits actually implemented. Preserve current budget semantics. Correct managed WORK provenance and stale plan paths while distinguishing implementation completion from acceptance pending. Do not rewrite archived reviewed spec bytes just for style. No new timeout CLI flag is required by this task.
