# Plan — question-matching-v2 review fix 3: the all-unavailable question is tested
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the one major of the supervision review of delivery `question-matching-v2-fixes-2-20261002-223317` (verdict FIX_BEFORE_SHIP): no test covers a question whose units are all unavailable. Test only; no production change; nothing in section 17 changes.
**Created:** 2026-10-02 · **Status:** approved

## Tasks
- [ ] 1. a question whose unit calls all fail is tested end to end — backend/low
      Scope: cmd/batuta/judge_questions_bench_test.go
      Accept: a fixture whose test server fails every unit call of a question yields a record with `best_unit` 0, status unavailable, `unavailable_calls` equal to the unit count, the question excluded from the criteria, and a run summary whose `unavailable_calls` equals that count → go test ./cmd/batuta -run TestQuestionsBenchAllUnitsUnavailable; no production file changes → git diff --quiet HEAD -- cmd/batuta/judge_questions_bench.go questions; the packages stay green → go test ./cmd/batuta ./questions

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`. Test file only.

Environment setup is never a question. For Go inside a sandbox on this machine: `HOME=/private/tmp/batuta-home GOCACHE=/private/tmp/batuta-gocache GOPATH=/Volumes/Home/francisross/go GOMODCACHE=/Volumes/Home/francisross/go/pkg/mod`, `GOTOOLCHAIN` stays `auto`. Run the tests named in the Accept lines. If the sandbox blocks a loopback listener, write the test and say so in the report: the conductor's gate runs the suite.

**Task 1.** Model the fixture on `TestQuestionsBenchUnitUnavailable` and `TestQuestionsBenchValidUnitBeatsUnavailable` in `cmd/batuta/judge_questions_bench_test.go` (~230–300); the summary is the last JSON object of the output, under the key `summary`.
