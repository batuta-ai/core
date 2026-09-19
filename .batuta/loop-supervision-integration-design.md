# Normal-loop supervision integration — approved direction

Date: 2026-09-16. Existing user direction: integrate active observation and post-delivery review into the normal loop experience; keep runner ownership and supervisory decisions separate internally; reserve an external watchdog for process availability only.

## Recommended slice

Compose the existing foreground runner and supervisor. Start the observer after the durable opening record exists. The runner remains the only execution owner. Observe progress while work runs; after the runner releases ownership, finalize observation and reuse the immutable review job. Resume and answer operations preserve cursor/job identity. Apply the same composition to roadmap phases. Retain standalone supervision for attach/recovery and preserve existing library callers without the new option.

Waiting for input without an authorized policy, blocked work, max-waves, cancellation and observer failures terminate predictably. Join owned goroutines/processes; unfinished work cannot start final review. Dry-run, dashboard and abandonment must never launch reviewers. Keep worker output and structured supervisor output separate. No daemon, installed service, automatic restart or chat notifications in this slice.

## Approved decision — 2026-09-16

The user approved blocking phase advancement when final review finds problems. Persist the gate across restarts and distinguish implementation completion from review pending/needs judgment. Missing, failed, incomplete or uncertain reviews also block. A complete intact SHIP review can clear the progression check, but is evidence rather than Git merge approval or a claim that a conductor accepted delivery. Findings require explicit evidence-bound judgment or independently verified correction; never silently discard them. A transient nonzero exit is insufficient because roadmap completion must not bypass the gate on resume.

## Candidate tasks and boundaries

1. Compose foreground execution and observation with deterministic private per-delivery cursor, synchronized startup and joined cleanup. Scope: loop/runner.go, loop/supervision_run.go, loop/supervision_run_test.go, loop/roadmap.go, loop/roadmap_test.go.
2. Wire new/resume/answer/roadmap CLI paths to the existing executable-backed review; preserve standalone supervision and nonexecuting commands. Scope: cmd/batuta/main.go, cmd/batuta/main_test.go, docs/loop.md, docs/loop-supervision.md, README.md, README.pt-BR.md.
3. If the recommended gate is selected, implement persistent review-pending/judgment state and roadmap recovery semantics. Additional scope: loop/report.go, loop/report_test.go and focused roadmap recovery tests. Split before implementation if durable state requires additional owners.

## Acceptance evidence

- New delivery emits observation events during work and one review after finalization and ownership release.
- Resume/answer replay causes neither duplicate worker execution nor duplicate review.
- Waiting input, blocked, max-waves and cancellation preserve established exit semantics and do not review unfinished work.
- Existing explicit-policy ownership, uncertainty and budget guards remain effective.
- Cancellation/observer failure joins all owned activity.
- Every roadmap phase is supervised; progression follows the selected durable acceptance rule.
- Run targeted race tests for supervision/roadmap, CLI loop/supervision tests, serialized full suite and build.

Tests use injected deterministic runners, not real model invocations. Keep this independent of production ACP qualification and dispatch token-savings measurements. No default authorization expansion or automatic review acceptance.
