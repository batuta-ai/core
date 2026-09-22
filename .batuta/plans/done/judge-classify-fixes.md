# Plan — judge-classify review fixes: escalation over every attempt, requests proven label-free, docs that match
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Close the five findings of the final review of delivery `judge-classify-20260922-100738` (verdict REWORK) on branch `feat/judge-classify`, each fix confined to the reviewed lines and covered by a test, so the classification bench measures reviewed code.
**Created:** 2026-09-22 · **Status:** done

## Tasks
- [x] 1. First-attempt outcome sees an escalation after a same-executor retry — backend/medium
      Scope: cmd/batuta/judge_classify.go, cmd/batuta/judge_classify_test.go
      Accept: benchOutcome walks every finished attempt after the first and returns escalated when any of them ran on a non-empty executor different from the first attempt's, retried when later attempts all ran on the same executor, candidate when the first attempt was a candidate, failed when no attempt became a candidate and none escalated → go test ./cmd/batuta -run TestClassifyBenchOutcome; a journal with three attempts on executors A, A, B reports escalated → go test ./cmd/batuta -run TestClassifyBenchOutcomeRetryThenEscalate; the package stays green → go test ./cmd/batuta
- [x] 2. Bench tests prove the requests carry neither label nor journal content — testing/medium
      Depends on: 1
      Scope: cmd/batuta/judge_classify_test.go
      Accept: TestClassifyBenchOutcome and TestClassifyBenchOutcomeUnknown keep the fake judge record and fail unless the number of request bodies equals the number of tasks and no body contains the journal delivery name, executor_started, or a complexity or domain key under task → go test ./cmd/batuta -run 'TestClassifyBenchOutcome$|TestClassifyBenchOutcomeUnknown'; the package stays green → go test ./cmd/batuta
- [x] 3. docs/judge.md describes classify as built — docs/low
      Scope: docs/judge.md
      Accept: the classify state is described as title, scope, accept, then the context paragraphs labelled for the task followed by the unlabelled Decisions paragraphs, with secret-shaped lines dropped and a 4000-byte bound → grep -q '4000' docs/judge.md; the plain-form example line starts with task_1 plan= and carries no slug → ! grep -n '^classify-bench task_1 plan=' docs/judge.md; the classify section no longer claims judge_intent or judge_result trace records for the CLI → ! grep -n 'Every question is also recorded as a `judge_intent` record' docs/judge.md; both classify forms stay documented → grep -q 'judge classify bench' docs/judge.md

## Decisions and context

Go standard library only, frozen exported signatures, conventional commits, no real judge calls in tests (use the existing fake judge `classifyTestServer`). Each fix stays inside the lines the review named; do not refactor neighbours. The review text for each finding is quoted below.

**Task 1.** Review finding, blocker, `cmd/batuta/judge_classify.go` `benchOutcome`: "benchOutcome only compares the second finished attempt (finished[1]) with the first. The loop doctrine (routing.ConductingFailurePolicy) is one retry on the same executor and only then an escalation: the typical journal of a task that escalated is finished [1,2,3] with executors [A,A,B]. The function comment and the plan's Decisions define escalated as a later attempt on another executor. TestClassifyBenchOutcome only builds a two-attempt jump (A then B), so the doctrine path is not exercised." Fix: walk all finished attempts after the first; return escalated if any has a non-empty executor different from the first, else retried. Add the three-attempt journal test.

**Task 2.** Review finding, major, `cmd/batuta/judge_classify_test.go` `TestClassifyBenchOutcome`: "It discards the fake judge bodies (`server, _ := classifyTestServer`)." Fix: `server, record := classifyTestServer(...)` in `TestClassifyBenchOutcome` and `TestClassifyBenchOutcomeUnknown`; fail unless `len(record.bodies)` equals the task count and no body contains the fixture delivery name, `executor_started`, or `task["complexity"]`/`task["domain"]` when decoded.

**Task 3.** Review findings, minor, `docs/judge.md` classify section: (a) "The state is described as title, scope, accept, and 'the plan context paragraphs that name the task'" — state the labelled paragraphs that apply to the task, then the unlabelled Decisions paragraphs, secret-shaped lines dropped, 4000-byte bound. (b) "The plain-form example is `classify-bench task_1 plan=testing/low …`" — use `task_1 plan=testing/low judge=testing/low complexity=0.90 domain=0.90 fallback=false input_tokens=12` under the plain form; keep slug, outcome and relation fields only in the bench section. (c) "The classify section says every request is recorded as `judge_intent` and `judge_result` trace records, in the same paragraph that says the bench writes nothing" — delete the classify trace sentence; traces belong to the live loop.
