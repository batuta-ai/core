# Plan — decoder-fallback-telemetry review fixes 2: known events from the real fixtures never count as dropped
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the findings of the claude/opus review of the fixes (verdict REWORK): cursor `thinking`/`user` and opencode `step_start` events, present in the committed fixtures, still count as dropped because the test lists handwritten events instead of replaying the fixtures; plus three small redaction and glob defects.
**Created:** 2026-09-25 · **Status:** done

## Tasks
- [x] 1. Every committed fixture replays with zero dropped lines; redaction and pruning edge cases are pinned — backend/medium
      Scope: executor/decode.go, executor/decode_test.go, executor/tail.go, executor/tail_test.go, review/report.go, review/report_test.go
      Accept: decoding every line of every file in executor/testdata/stream/*.jsonl and executor/testdata/stream/errors/*.jsonl reports DroppedLines()==0, and one synthetic unknown event per format still counts 1 → go test ./executor -run TestDecoderFixturesDropNothing; cursor treats `thinking` and `user` as known no-op events and opencode treats `step_start` and `tool_use` as known no-op events → go test ./executor -run TestDecoderFixturesDropNothing; path redaction replaces the workspace prefix only when it is followed by a separator, a quote, whitespace or the end of the string, so `/work/spacefoo` stays when the workspace is `/work/space` → go test ./executor -run TestRedactWorkspaceBoundary; stale cohort tails are found with os.ReadDir plus filepath.Match on the entry name, so glob metacharacters in the artifact directory are literal → go test ./review -run TestWriteArtifactsPrunesStaleTails; a table test pins the review tail redaction: `a/b/c`, `./x/y` and `https://host/path` stay, an absolute path under the workspace loses the workspace prefix → go test ./review -run TestReviewTailRedaction; the packages stay green → go test ./executor ./review ./loop

## Decisions and context

Go standard library only, conventional commits. Decoders are in `executor/decode.go` (cursor ~109, opencode ~359); the shared redactor is `executor/tail.go` (~44); pruning is `review/report.go` (~217) and the review tail is ~236. Sandbox note: the sandbox blocks the Go build cache; write the code and tests, do not work around the sandbox, and let the conductor's gate run the proofs.
