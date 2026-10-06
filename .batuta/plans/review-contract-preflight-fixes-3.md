# Plan — review contract and capability preflight: fixes from the full branch review
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the accepted findings of `batuta review --base main --full` over branch `feat/review-contract-preflight` on 2026-10-06: executor tails drop lines that embed a secret, the probe tail falls back to raw output, review failure tails redact each stream on its own, the no-tests proof rule knows `[no test files]` and the coverage form, the loop probes a route over the route's own transport, a canceled dispatch preflight stays interrupted, the usage text names exit 4, and a supervision review job stays within its read limit.
**Created:** 2026-10-06 · **Status:** approved

## Tasks
- [ ] 1. executor tails drop secret-bearing lines and the probe tail falls back to raw output — backend/medium
      Scope: executor/tail.go, executor/tail_test.go, executor/capability.go, executor/capability_test.go
      Accept: a new exported helper in `executor/tail.go` removes every line that contains a secret-shaped assignment anywhere in it (for example `provider error: OPENAI_API_KEY=sk-test-123`) and keeps the other lines; `DropSecretLines` and `IsSecretLine` keep their behaviour → go test ./executor -run TestDropSecretBearingLines -v 2>&1 | grep -q -- '--- PASS: TestDropSecretBearingLines'; the probe tail applies that helper to decoded stdout, then to stderr, then to bounded raw stdout when the earlier sources are empty → go test ./executor -run TestProbeTailSources -v 2>&1 | grep -q -- '--- PASS: TestProbeTailSources'
- [ ] 2. review failure tails redact stdout and stderr separately — backend/medium
      Depends on: 1
      Scope: review/report.go, review/report_test.go
      Accept: a review failure whose stdout ends without a newline and whose stderr starts with a secret-shaped assignment carries a tail without that secret, because each stream is tailed and redacted on its own (with the task 1 helper) and only then joined by a newline under the existing size bound → go test ./review -run TestReviewFailureTailSeparateStreams -v 2>&1 | grep -q -- '--- PASS: TestReviewFailureTailSeparateStreams'; the existing tail tests stay green → go test ./review -run 'TestReviewFailureTail' -v 2>&1 | grep -q -- '--- PASS: TestReviewFailureTailRawFallback'
- [ ] 3. the no-tests proof rule knows `[no test files]` and the coverage form — backend/medium
      Scope: gates/gates.go, gates/gates_test.go
      Accept: a proof exiting 0 whose output is only `? <pkg> [no test files]` lines, or `ok <pkg> <time> coverage: <text> [no tests to run]` lines, fails as no test ran → go test ./gates -run TestProofsNoTestFiles -v 2>&1 | grep -q -- '--- PASS: TestProofsNoTestFiles'; a proof mixing those lines with a package that ran tests (`ok <pkg> <time>` with or without a coverage suffix, or `--- PASS:` lines) passes → go test ./gates -run TestProofsMixedNoTestFiles -v 2>&1 | grep -q -- '--- PASS: TestProofsMixedNoTestFiles'
- [ ] 4. the loop probes a route over the route's own transport — backend/high
      Scope: loop/capability.go, loop/capability_test.go, loop/runner.go, loop/attempt.go, loop/loop_test.go, docs/loop.md
      Accept: a route whose recorded transport is ACP is probed through the ACP backend the task would use, and a CLI route through the CLI backend; the `capability_probe` record's transport is the one that ran the probe → go test ./loop -run TestLoopProbeUsesRouteTransport -v 2>&1 | grep -q -- '--- PASS: TestLoopProbeUsesRouteTransport'; with `--transport auto`, a route that falls back to CLI before submission is probed over CLI and recorded as such → go test ./loop -run TestLoopProbeAutoFallback -v 2>&1 | grep -q -- '--- PASS: TestLoopProbeAutoFallback'; the existing capability tests stay green → go test ./loop -run 'TestLoopProbe|TestLoopIncapable' -v 2>&1 | grep -q -- '--- PASS: TestLoopProbesRouteOnce'
- [ ] 5. a canceled dispatch preflight stays interrupted, and the usage text names exit 4 — backend/medium
      Scope: executor/dispatch.go, executor/dispatch_test.go, cmd/batuta/main.go, cmd/batuta/main_test.go
      Accept: canceling the parent context while the preflight probe runs ends the dispatch with the interrupted exit class and code 130, not `unavailable` → go test ./executor -run TestDispatchPreflightCanceled -v 2>&1 | grep -q -- '--- PASS: TestDispatchPreflightCanceled'; a probe that cannot start still ends `unavailable` with code 2 → go test ./executor -run TestDispatchPreflightUnavailable -v 2>&1 | grep -q -- '--- PASS: TestDispatchPreflightUnavailable'; the review section of the built-in usage text lists exit 4 `review_incomplete` and `review_failures.json` → go test ./cmd/batuta -run TestUsageNamesReviewExit4 -v 2>&1 | grep -q -- '--- PASS: TestUsageNamesReviewExit4'
- [ ] 6. a supervision review job stays within its read limit — backend/medium
      Scope: loop/supervision_review.go, loop/supervision_review_test.go
      Accept: a review whose `review_failures.json` holds enough failures that copying their full tails into `job.json` would pass the 1 MiB read limit produces a `job.json` that loads again, with the failures kept (kind, cohort, reason, exit code) and their tails bounded or dropped so the file fits → go test ./loop -run TestSupervisionReviewJobWithinLimit -v 2>&1 | grep -q -- '--- PASS: TestSupervisionReviewJobWithinLimit'; the existing supervision review tests stay green → go test ./loop -run 'TestSupervisionReview' -v 2>&1 | grep -q -- '--- PASS: TestSupervisionReviewFailuresRecorded'

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`, temp dirs through `tempDir(t)`. `cmd/batuta` must build on linux, darwin and windows. Every CLI form prints compact JSON or TSV. Release tooling (`CHANGELOG.md`, `.release-please-manifest.json`, `release-please-config.json`, `.goreleaser.yaml`, `.github/workflows/*`) is never touched.

Environment setup is never a question. `GOCACHE` is already set to a writable directory in the executor's environment: run `go test` plainly, with no `GOCACHE=` prefix. Run the tests named in your task's Accept lines. If the sandbox blocks a loopback listener, write the tests and say so in the report: the conductor's gate runs the whole suite.

Every proof greps the `-v` output for a named `--- PASS:` line because the binary running this loop predates the no-tests rule; each named test must exist with exactly that name.

Declined, do not implement: decoding escaped JSON field names (for example `"token"`) before redaction. The redaction targets accidental leaks, not output crafted to evade it; this stays a known limit.

**Task 1.** `executor/tail.go` has `IsSecretLine` (a line that starts with `[A-Z][A-Z0-9_]*=` after trimming), `DropSecretLines`, `RedactPaths` and `Tail`; keep them as they are, other packages rely on them. `probeTail` (`executor/capability.go` ~130–140) reads decoded `Result.Stdout`, then `Result.Stderr`; `Result.RawStdout` is the undecoded stream.

**Task 2.** `newReviewFailure` (`review/report.go` ~45–58) joins stdout (or raw stdout) and stderr with no separator and then calls `reviewOutputTail` (~349), which tails, path-redacts, drops secret lines and bounds to 4096 bytes.

**Task 3.** `noTestsSignal`, `testsRanSignal` and `noTestsRan` sit above `gates.Proofs` (`gates/gates.go` ~345–360). Real `go test` output on this machine: `ok <pkg> 0.15s [no tests to run]` when no test matches, `? <pkg> [no test files]` for a package without tests, and with `-cover` the summary carries `coverage: …` before the suffix.

**Task 4.** `prepare` wires `probeBackend := executor.CLIBackend{…}` and `backend := loopTransport(opts.Transport, probeBackend)` (`loop/runner.go` ~472–490); `probeRoute` (`loop/capability.go` ~60) always calls `executor.ProbeCapability` with `r.probeBackend`. A probe over ACP must not reuse the task's ACP session and must leave no session behind.

**Task 5.** `runPreflight` (`executor/dispatch.go` ~233) maps every probe error to `unavailable`/2; dispatch already maps interrupt and timeout to 130 and 124 elsewhere in `Dispatch`. The usage text for `review` is in `cmd/batuta/main.go` ~141–142.

**Task 6.** `ReviewFailures` are copied into the review job (`loop/supervision_review.go` ~42); `job.json` is read back with a 1 MiB limit (~546), while each failure tail can reach 4096 bytes and the artifact reader accepts far more.

After the plan, by the conductor: `batuta review --base main --full --spec .batuta/plans/done/review-contract-preflight.md` over the whole branch, then the PR; remaining findings become issues.
