# Plan — question-matching-v2 review fix 4: an unavailable question is excluded from the Jev decision and counted
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the major of the supervision review of delivery `question-matching-v2-fixes-3-20261002-224218` (verdict FIX_BEFORE_SHIP) and the gap it exposes: section 17 says a question with more than 10% of its units unavailable is unavailable and excluded from the decision, but `questionsBenchCountRecords` still counts such a question in the criterion 2 totals, and `questions_excluded` does not count it. Code kinds need no judge call, so criterion 1 and the confusion table keep every question. Nothing in section 17 changes.
**Created:** 2026-10-02 · **Status:** approved

## Tasks
- [ ] 1. an unavailable question leaves the criterion 2 totals and is counted as excluded; the confusion table keeps it — backend/medium
      Scope: cmd/batuta/judge_questions_bench.go, cmd/batuta/judge_questions_bench_test.go, docs/judge.md
      Accept: a question whose record is unavailable, in full or units mode, is not counted in the criterion 2 totals for yes or for no, in the run summary and per split → go test ./cmd/batuta -run TestQuestionsBenchUnavailableLeavesCriterion2; the summary's `questions_excluded` counts the questions without a plan plus the unavailable questions, and `unavailable` keeps counting the unavailable ones → go test ./cmd/batuta -run TestQuestionsBenchExcludedCounts; an unavailable question still appears in the confusion table and in criterion 1 with its code kind and label → go test ./cmd/batuta -run TestQuestionsBenchUnavailableKeepsCriterion1; the all-unavailable fixture asserts all of the above for a question whose label contributes to criterion 1 → go test ./cmd/batuta -run TestQuestionsBenchAllUnitsUnavailable; the earlier bench cases still hold → go test ./cmd/batuta -run 'TestQuestionsBench'; docs/judge.md says an unavailable question is excluded from criterion 2 and kept in criterion 1 → grep -q 'excluded from criterion 2' docs/judge.md; the packages stay green → go test ./cmd/batuta ./questions

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`. Keep the task to the Accept lines: no refactor, no renamed field, no change to `questions/` or to section 17.

Environment setup is never a question. For Go inside a sandbox on this machine: `HOME=/private/tmp/batuta-home GOCACHE=/private/tmp/batuta-gocache GOPATH=/Volumes/Home/francisross/go GOMODCACHE=/Volumes/Home/francisross/go/pkg/mod`, `GOTOOLCHAIN` stays `auto`. Run the tests named in the Accept lines. If the sandbox blocks the Go build cache or a loopback listener, write the tests and say so in the report: the conductor's gate runs the whole suite.

**Task 1.** The counting is `questionsBenchCountRecords` (`cmd/batuta/judge_questions_bench.go` ~420–470): the confusion table and `Unavailable` are counted before the `other` filter, then criterion 2 totals; add the exclusion there, after the confusion table. `questions_excluded` is set in `questionsBenchRecordsMode` (~204–255) from the plan-not-found count; add the unavailable records to it in the run summary and per split. The all-unavailable fixture is `TestQuestionsBenchAllUnitsUnavailable` (~272 in the test file).
