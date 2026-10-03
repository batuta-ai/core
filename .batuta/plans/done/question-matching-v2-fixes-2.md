# Plan — question-matching-v2 review fix 2: a valid unit always beats an unavailable one; the summary's unavailable count is asserted
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the two findings of the supervision review of delivery `question-matching-v2-fixes-20261002-222354` (verdict REWORK): an unavailable first unit stays the best unit when a later valid unit also scores zero, because the comparison is a strict greater-than; and no test asserts the run summary's unavailable call count. Nothing in section 17 changes.
**Created:** 2026-10-02 · **Status:** done

## Tasks
- [x] 1. the best unit is the first valid unit over any unavailable unit, and the summary's unavailable count is tested — backend/medium
      Scope: cmd/batuta/judge_questions_bench.go, cmd/batuta/judge_questions_bench_test.go
      Accept: when the first unit is unavailable and a later unit answers with P(`answered_here`) equal to zero, the later unit is the best unit and the question's status is that unit's status, not unavailable → go test ./cmd/batuta -run TestQuestionsBenchValidUnitBeatsUnavailable; among valid units the highest P(`answered_here`) wins and ties go to the earlier unit, as before → go test ./cmd/batuta -run TestQuestionsBenchBestUnit; a question whose units are all unavailable keeps best unit 0 and the status unavailable → go test ./cmd/batuta -run TestQuestionsBenchUnitUnavailable; the mismatch test parses the final summary object of the JSON output and asserts its `unavailable_calls` equals the number of failed unit calls → go test ./cmd/batuta -run TestQuestionsBenchUnitMismatchUnavailable; the packages stay green → go test ./cmd/batuta ./questions

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`. Keep the task to the Accept lines: no refactor, no renamed field, no change to `questions/` or to section 17.

Environment setup is never a question. For Go inside a sandbox on this machine: `HOME=/private/tmp/batuta-home GOCACHE=/private/tmp/batuta-gocache GOPATH=/Volumes/Home/francisross/go GOMODCACHE=/Volumes/Home/francisross/go/pkg/mod`, `GOTOOLCHAIN` stays `auto`. Run the tests named in the Accept lines. If the sandbox blocks the Go build cache or a loopback listener, write the tests and say so in the report: the conductor's gate runs the whole suite.

**Task 1.** The unit loop is in `cmd/batuta/judge_questions_bench.go` ~330–370. Track whether the current best unit is valid; a valid unit replaces an unavailable best unit regardless of probability, and replaces a valid best unit only on a strictly higher P(`answered_here`). The mismatch test is `TestQuestionsBenchUnitMismatchUnavailable` (~230 in the test file); the summary is the last JSON object of the output, under the key `summary`.
