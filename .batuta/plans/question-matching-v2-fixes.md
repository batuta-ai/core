# Plan — question-matching-v2 review fix: a unit call that returns an error is unavailable, whatever answer it carries
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the one blocker of the supervision review of delivery `question-matching-v2-20261002-215013` (verdict REWORK): in unit mode the bench treats an `answer_mismatch` error that still carries a usable `answer` as a successful call. Section 17 says a unit whose call is unavailable scores zero and is counted; every non-nil error is such a call. Nothing in section 17 changes.
**Created:** 2026-10-02 · **Status:** approved

## Tasks
- [ ] 1. unit mode treats every call that returns an error as unavailable — backend/medium
      Scope: cmd/batuta/judge_questions_bench.go, cmd/batuta/judge_questions_bench_test.go
      Accept: in unit mode a call whose `Ask` returns a non-nil error, including an `answer_mismatch` with a usable `answer`, has the status unavailable, scores zero for P(`answered_here`), is counted in the unit and run unavailable counts, is never the best unit over a unit that answered, and its input-token usage is still added → go test ./cmd/batuta -run TestQuestionsBenchUnitMismatchUnavailable; the earlier unit-mode cases still hold → go test ./cmd/batuta -run 'TestQuestionsBenchUnit'; full-passage mode keeps its existing handling of a usable answer on `answer_mismatch` → go test ./cmd/batuta -run TestQuestionsBenchUnavailable; the packages stay green → go test ./cmd/batuta ./questions

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`. Keep the task to the Accept lines: no refactor, no renamed field, no change to `questions/` or to section 17.

Environment setup is never a question. For Go inside a sandbox on this machine: `HOME=/private/tmp/batuta-home GOCACHE=/private/tmp/batuta-gocache GOPATH=/Volumes/Home/francisross/go GOMODCACHE=/Volumes/Home/francisross/go/pkg/mod`, `GOTOOLCHAIN` stays `auto`. Run the tests named in the Accept lines. If the sandbox blocks the Go build cache or a loopback listener, write the tests and say so in the report: the conductor's gate runs the whole suite.

**Task 1.** The unit loop is in `cmd/batuta/judge_questions_bench.go` ~330–360: the condition on `err` admits an `answer_mismatch` with a usable answer. In unit mode the rule is simply `err != nil`; the usage line before it stays. The full-passage path above it is not changed. The test server of the existing unit tests answers per request body; add a body that returns a valid `answer` plus an extra answer key so `HTTPJudge` reports `answer_mismatch`.
