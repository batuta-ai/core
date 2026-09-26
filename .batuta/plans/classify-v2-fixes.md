# Plan — classify-v2 review fix: a partial answer set reaches the frozen defaults
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the blocker of the supervision review of delivery `classify-v2-20260926-172530` (verdict REWORK). Section 13 of `.batuta/judge-research.md` says an answer that is missing or below 0.7 confidence takes the conservative default. `judge.HTTPJudge` rejects the whole response with `answer_mismatch` when one of the five answers is missing or malformed, so the bench counts the task unavailable and the defaults never apply. A response with no usable answer at all stays unavailable, as section 13 also says.
**Created:** 2026-09-26 · **Status:** approved

## Tasks
- [ ] 1. HTTPJudge keeps the valid answers of a mismatched response; bench v2 applies DecideV2 to them — backend/high
      Scope: judge/http.go, judge/http_test.go, judge/chain.go, judge/chain_test.go, cmd/batuta/judge_classify.go, cmd/batuta/judge_classify_test.go
      Accept: when validation fails with `answer_mismatch`, HTTPJudge.Ask returns a Response holding only the answers that match their question's key and type (and, for choice, a known option) together with the same error → go test ./judge -run TestHTTPJudgePartialAnswersOnMismatch; Chain.Ask, when every provider fails, returns the partial Response of the last provider that failed with `answer_mismatch`, if any, with the chain error → go test ./judge -run TestChainReturnsPartialOnMismatch; the bench with `--rubric v2`, on an `answer_mismatch` error with at least one usable answer, applies DecideV2 to the usable answers, marks the missing ones as defaulted, records the reason, and counts the task in the measures → go test ./cmd/batuta -run TestClassifyBenchV2PartialAnswers; on any other error, or on `answer_mismatch` with no usable answer, the task stays unavailable and outside the measures → go test ./cmd/batuta -run TestClassifyBenchV2Unavailable; callers that check the error first (claim_evidence, v1 classify) behave as before → go test ./loop ./classify && go test ./cmd/batuta -run 'TestJudgeClassify|TestClassifyBench$'; the packages stay green → go test ./judge ./cmd/batuta ./loop ./classify

## Decisions and context

Go standard library only, conventional commits. Every existing caller of `Ask` treats a non-nil error as unavailable before reading the response, so returning answers alongside an error changes nothing for them; keep it that way. `validateAnswers` is `judge/http.go` ~183; `Chain.Ask` is `judge/chain.go` ~85; the v2 bench path is `cmd/batuta/judge_classify.go` ~625–640. Sandbox note: the test HTTP server may need loopback, which the sandbox can block; write the tests and let the conductor's gate run them.
