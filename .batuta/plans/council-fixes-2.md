# Plan — batuta council: parse what real counsellors print
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** The first run of `batuta council` with real executors (2026-10-08) rejected three correct answers and lost the whole cross-review stage, yet still recommended APPROVE. The parsers accept the shapes real counsellors print, and a council whose cross-review stage did not parse for at least two counsellors is incomplete.
**Created:** 2026-10-08 · **Status:** approved

## Tasks
- [ ] 1. council parsers accept real counsellor output — backend/high
      Scope: council/parse.go, council/parse_test.go, council/prompt.go, council/prompt_test.go, council/testdata/claude-critique.txt, council/testdata/agy-cross-review.txt, council/testdata/codex-cross-review.txt
      Accept: the three recorded answers, copied verbatim into `council/testdata/`, parse: the claude critique with its three findings and verdict REVISE despite the provider line after the verdict, the agy cross-review with its four votes and ranking `C`, the codex cross-review with its ranking `A` and no votes because critique A had no findings → go test ./council -run TestParseRecordedAnswers; the critique parser ignores lines after the block that are not a verdict and fails on two different verdicts or none → go test ./council -run TestParseCritiqueTrailingLines; the cross-review parser ignores blank lines and prose outside votes and ranking entries, takes votes from lines `<id>: AGREE|DISAGREE` before `FINAL RANKING:`, accepts ranking entries `N. X` and `N. Critique X`, ends the ranking at the first line that is not an entry, and still rejects an unknown id, a repeated vote or label, a missing vote for a shown finding and a ranking that omits a shown label → go test ./council -run TestParseCrossReviewTolerant; the stage-2 prompt shows the exact vote and ranking lines with an example, says a critique without findings needs no votes, and says to print nothing after the ranking → go test ./council -run TestCrossReviewPromptFormatExample
- [ ] 2. a council without two parsed cross-reviews is incomplete — backend/medium
      Depends on: 1
      Scope: council/aggregate.go, council/aggregate_test.go, council/run.go, council/run_test.go, cmd/batuta/council.go, cmd/batuta/council_test.go, docs/council.md
      Accept: when fewer than two cross-reviews parsed, the aggregate's recommendation is INCOMPLETE, `council.md` says so above the synthesis, and `batuta council` exits 4 like a council with fewer than two critiques → go test ./cmd/batuta -run TestCouncilIncompleteCrossReview; with two or more parsed cross-reviews, APPROVE and REVISE follow the existing rule and exits stay 0 and 2 → go test ./cmd/batuta -run TestCouncilExitCodes; docs/council.md states both incompleteness rules → grep -q 'cross-review' docs/council.md

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`, temp dirs through `tempDir(t)`. `cmd/batuta` must build on linux, darwin and windows. Release tooling is never touched. Tests use fake adapters and fake subprocess runners; no test calls a real model.

Environment setup is never a question. `GOCACHE` is set to a writable directory and the Go 1.26.4 toolchain is first on PATH with `GOTOOLCHAIN=local`: run `go test` plainly. If your shell still has `GOROOT` pointing to the mise Go 1.24.2 install, run `env -u GOROOT go test …`; if that is refused too, finish the code and tests, say so in your report and stop — the conductor's gate runs the suite.

The recorded answers are in `.batuta/council-smoke/2026-10-08/` (`claude-critique.txt`, `agy-cross-review.txt`, `codex-cross-review.txt`, and `README.json` with the label map: A agy, B claude, C codex). Copy the three text files byte for byte into `council/testdata/`; do not edit them. In that run critique B did not parse, so the cross-review stage showed only critiques A and C: agy (A) reviewed C's four findings `C1`–`C4`, and codex (C) reviewed A, which had no findings. The codex tail also ends with a line its CLI printed about reading stdin, outside the ranking.

**Task 1.** `ParseCritique` (`council/parse.go` ~67) requires the last line to be the verdict; `ParseCrossReview` (~140–195) treats every line before `FINAL RANKING:` as a vote (so a blank line or a sentence fails), and accepts ranking entries only as `N. X`. The stage-2 prompt is in `council/prompt.go` (~33). Tolerance must not weaken the structure: a vote or ranking entry that is present must still be exact.

**Task 2.** `council.Run` continues while at least two critiques parsed; nothing checks the cross-review stage. The command maps recommendations to exits in `cmd/batuta/council.go` (0 APPROVE, 2 REVISE, 4 incomplete, 1 error).

Lesson from the same run, for later plans: a proof such as `go test … | grep -q '--- PASS: X'` takes grep's exit code and hides a failing test in the same run; core `v1.1.0-beta.50` fails a proof whose runner ran no test, so proofs here are plain `go test -run <name>`.
