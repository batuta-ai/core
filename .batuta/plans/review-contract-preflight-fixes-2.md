# Plan — review contract and capability preflight: fixes from the second delivery review
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the three blockers of the final review of delivery `review-contract-preflight-fixes-20261006-125827`: the capability probe fails when it cannot read the tree's status, the no-tests proof rule fails only a proof in which no package ran a test, and the raw-output fallback of review failure tails never carries a secret value embedded in a JSON event.
**Created:** 2026-10-06 · **Status:** approved

## Tasks
- [ ] 1. the capability probe fails when the initial tree status cannot be read — backend/medium
      Scope: executor/capability.go, executor/capability_test.go
      Accept: when `git status --porcelain` cannot be read in the directory before the probe runs, the probe fails with reason `no_repository` even when `git rev-parse HEAD` succeeds, and never passes without a tree check → go test ./executor -run TestProbeCapabilityStatusUnreadable -v 2>&1 | grep -q -- '--- PASS: TestProbeCapabilityStatusUnreadable'; the existing probe tests stay green → go test ./executor -run 'TestProbeCapability' -v 2>&1 | grep -q -- '--- PASS: TestProbeCapabilityRequiresHead'
- [ ] 2. a multi-package proof fails as "no test ran" only when no package ran a test — backend/medium
      Scope: gates/gates.go, gates/gates_test.go
      Accept: a proof exiting 0 whose Go output has one package line `ok <pkg> <time> [no tests to run]` and another package line `ok <pkg> <time>` (or `--- PASS:` lines) passes → go test ./gates -run TestProofsMixedPackages -v 2>&1 | grep -q -- '--- PASS: TestProofsMixedPackages'; a proof where every package reports `[no tests to run]`, or the single-package `testing: warning: no tests to run` output with no `--- PASS:` line, still fails → go test ./gates -run TestProofsNoTestsRan -v 2>&1 | grep -q -- '--- PASS: TestProofsNoTestsRan'; the Jest `No tests found, exiting with code 0` case still fails → go test ./gates -run TestProofsNoTestsRan -v 2>&1 | grep -q -- '--- PASS: TestProofsNoTestsRan'
- [ ] 3. review failure tails never carry a secret value from raw JSON events — backend/medium
      Scope: review/report.go, review/report_test.go
      Accept: a review failure whose tail falls back to raw stdout holding JSON event lines with a secret-shaped assignment inside a string (for example `{"type":"text","text":"API_KEY=sk-example"}`) or a field whose key names a secret (`token`, `api_key`, `password`, `secret`, case-insensitive) carries neither the secret value nor the line that held it → go test ./review -run TestReviewFailureTailRawSecrets -v 2>&1 | grep -q -- '--- PASS: TestReviewFailureTailRawSecrets'; the raw-fallback and decoded-tail tests stay green → go test ./review -run 'TestReviewFailureTail' -v 2>&1 | grep -q -- '--- PASS: TestReviewFailureTailRawFallback'

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`, temp dirs through `tempDir(t)`. `cmd/batuta` must build on linux, darwin and windows. Release tooling (`CHANGELOG.md`, `.release-please-manifest.json`, `release-please-config.json`, `.goreleaser.yaml`, `.github/workflows/*`) is never touched.

Environment setup is never a question. `GOCACHE` is already set to a writable directory in the executor's environment: run `go test` plainly, with no `GOCACHE=` prefix. Run the tests named in your task's Accept lines. If the sandbox blocks a loopback listener, write the tests and say so in the report: the conductor's gate runs the whole suite.

Every proof greps the `-v` output for a named `--- PASS:` line because the binary running this loop predates the no-tests rule; each named test must exist with exactly that name.

**Task 1.** `ProbeCapability` (`executor/capability.go` ~60–80) reads `before, tracked := porcelainStatus(ctx, dir)` and `head, hasHead := headSHA(ctx, dir)` before running the executor; the tree check runs only `if tracked`, so an unreadable status skips it while a readable `HEAD` still lets the probe pass.

**Task 2.** `noTestsRan` (`gates/gates.go` ~350) matches any single line, so `go test ./a ./b -run X` fails when one package has no matching test and the other ran tests; proofs of that shape exist in the plans (`go test ./executor ./loop -run 'Probe|…'`). `gates.Proofs` serves the loop, `batuta gate proofs` and the review's spec sweep.

**Task 3.** `newReviewFailure` (`review/report.go` ~45–58) falls back to `Result.RawStdout`, the undecoded stream (JSON events for stream-json adapters), when decoded `Stdout` is empty; `reviewOutputTail` (~349) applies `executor.DropSecretLines`, which only drops lines that start with `KEY=`. Other redaction helpers exist for reference: `executor/acp/permission.go` (`secretKey`, `redactDenialField`) and `inventory/redact.go`. Keep the fix inside `review`; do not change the executor helpers.

After the plan, by the conductor: `batuta review --base main --full --spec .batuta/plans/review-contract-preflight.md` over the whole branch, then the PR.
