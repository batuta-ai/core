# Plan — executor-telemetry review fixes: redact before cutting, a proof that only the tail is read
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Close the two majors of the final review of delivery `executor-telemetry-20260922-154512` (verdict FIX_BEFORE_SHIP) on branch `feat/executor-telemetry`, each fix confined to the reviewed lines and covered by a test.
**Created:** 2026-09-22 · **Status:** done

## Tasks
- [x] 1. The output tail is redacted before it is cut to 4096 bytes — backend/medium
      Scope: loop/attempt.go, loop/finished_telemetry_test.go
      Accept: outputTailDetail drops secret-shaped lines and redacts workspace paths on the whole 40-line tail first, and only then keeps the last 4096 bytes on a UTF-8 boundary, starting the kept text at a line start when one exists inside the window → go test ./loop -run TestFinishedTailRedactedBeforeCut; a tail whose 4096-byte window would begin in the middle of a long API_KEY=secret line or of an absolute workspace path records neither the secret nor the workspace prefix → go test ./loop -run TestFinishedTailRedactedBeforeCut; the existing tail tests stay green → go test ./loop -run 'TestFinished|TestLimitWait'; the package stays green → go test ./loop
- [x] 2. The usage proof fails if Outcome reads more than the last 20 lines — testing/medium
      Scope: executor/usage_test.go
      Accept: TestOutcomeUsageFromTail plants a matching decoy with different counters before the last 20 lines of stdout (INPUT: 1 OUTPUT: 1 CACHED: 1) and of stderr (total: 1) and asserts the extracted counters come from the lines inside the tail → go test ./executor -run TestOutcomeUsageFromTail; the package stays green → go test ./executor

## Decisions and context

Go standard library only, conventional commits. Each fix stays inside the lines the review named.

**Task 1.** Review finding, major, `loop/attempt.go` `outputTailDetail`: "outputTailDetail cuts each stream to a 4096-byte suffix (tailBytes) before dropSecretLines/redactText. dropSecretLines only drops complete lines matching ^[A-Z][A-Z0-9_]*= (loop/judgment.go); redactText only strips a workspace prefix or an absolute-path match that still starts with / or \\. tailBytes may start mid-line, so KEY= and the workspace prefix can fall outside the suffix. The same package already drops claimEnvLine matches before applying line/byte caps in boundExecutorReport. TestFinishedTailRedacted only uses a short API_KEY line that remains a complete line after Tail." Fix: redact the full 40-line tail, then cut; after the byte cut, drop a leading partial line when the window contains a newline. Read `boundExecutorReport` in `loop/judgment.go` for the existing order; do not edit that file.

**Task 2.** Review finding, major, `executor/usage_test.go` `TestOutcomeUsageFromTail`: "both streams put the only matching usage line at the end ... so the last-line match succeeds whether Outcome uses Tail(..., 20) or the full streams." Fix only the test: plant decoys in the dropped prefix so reading the full streams would pick the decoy.
