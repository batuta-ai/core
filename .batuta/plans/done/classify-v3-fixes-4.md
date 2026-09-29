# Plan — classify-v3 review fixes 4: the calibrate reader checks the values of unanswered entries and of each probability
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the two blockers of the supervision review of delivery `classify-v3-fixes-3-20260928-154826` (verdict REWORK): an answer with the status not_asked or unavailable skips every value check, and a null probability value is accepted. Nothing in `.batuta/judge-research.md` section 14 changes.
**Created:** 2026-09-28 · **Status:** done

## Tasks
- [x] 1. judge classify calibrate checks unanswered entries and every probability value — backend/medium
      Scope: cmd/batuta/judge_classify_calibrate.go, cmd/batuta/judge_classify_calibrate_test.go
      Accept: an answer whose status is not_asked or unavailable is rejected when its `confidence` is missing, null, not a number or not zero, and when it carries a `choice` or a `probabilities` key → go test ./cmd/batuta -run TestClassifyCalibrateUnansweredEntry; an answer whose status is firm, insufficient or below_threshold is rejected when a value in its `probabilities` is null, not a number or outside 0 to 1, or when a key in its `probabilities` is not one of the three options of its question → go test ./cmd/batuta -run TestClassifyCalibrateProbabilityValues; an answer is rejected when its status is insufficient and its `choice` is not `insufficient`, or when its status is firm or below_threshold and its `choice` is `insufficient` → go test ./cmd/batuta -run TestClassifyCalibrateStatusChoiceConsistency; each rejection exits 1 and names the 1-based line and the field → go test ./cmd/batuta -run 'TestClassifyCalibrateUnansweredEntry|TestClassifyCalibrateProbabilityValues|TestClassifyCalibrateStatusChoiceConsistency'; the earlier cases still hold → go test ./cmd/batuta -run 'TestClassifyCalibrate'; records written by the real bench for every status pass the validation unchanged → go test ./cmd/batuta -run TestClassifyCalibrateAcceptsBenchOutput; no file outside the calibrate reader and its test changes → git diff --quiet HEAD -- cmd/batuta/judge_classify_v3.go classify docs; the package stays green → go test ./cmd/batuta

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`. Keep the task to the Accept lines: no refactor, no renamed field, no change to the bench, to `classify/` or to the docs. `.batuta/judge-research.md` section 14 is frozen.

Environment setup is never a question. For Go inside a sandbox on this machine: `HOME=/private/tmp/batuta-home GOCACHE=/private/tmp/batuta-gocache GOPATH=/Volumes/Home/francisross/go GOMODCACHE=/Volumes/Home/francisross/go/pkg/mod`, `GOTOOLCHAIN` stays `auto`. Run the tests named in the Accept lines. If the sandbox blocks the Go build cache or a loopback listener, write the tests and say so in the report: the conductor's gate runs the whole suite.

**Task 1.** The answer checks are in `validateCalibrateRecordFields` (`cmd/batuta/judge_classify_calibrate.go` ~292–320): the `continue` on a status outside `calibrateAnsweredStatuses` is where the unanswered checks go. The probability loop is `calibrateValidateProbabilities` (~430–455); it needs the question to know the allowed keys, which are in `calibrateQuestionChoices`. `TestClassifyCalibrateAcceptsBenchOutput` already exists and must keep passing without being weakened.
