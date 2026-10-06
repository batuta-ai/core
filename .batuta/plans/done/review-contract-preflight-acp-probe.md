# Plan — the loop probes a route over the route's own transport
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Task 4 of `.batuta/plans/review-contract-preflight-fixes-3.md`, re-planned with the ACP test fixtures in Scope: the capability probe runs over the transport the task will use, ACP or CLI, and the `capability_probe` record names the transport that ran it.
**Created:** 2026-10-06 · **Status:** done

## Tasks
- [x] 1. the loop probes a route over the route's own transport — backend/high
      Scope: loop/capability.go, loop/capability_test.go, loop/runner.go, loop/attempt.go, loop/loop_test.go, loop/attempt_test.go, loop/settle_test.go, loop/finished_telemetry_test.go, loop/supervision_policy_test.go, loop/supervision_test.go, cmd/batuta/main_test.go, docs/loop.md
      Accept: a route whose recorded transport is ACP is probed through the ACP backend the task would use, and a CLI route through the CLI backend; the `capability_probe` record's transport is the one that ran the probe → go test ./loop -run TestLoopProbeUsesRouteTransport -v 2>&1 | grep -q -- '--- PASS: TestLoopProbeUsesRouteTransport'; with `--transport auto`, a route that falls back to CLI before submission is probed over CLI and recorded as such → go test ./loop -run TestLoopProbeAutoFallback -v 2>&1 | grep -q -- '--- PASS: TestLoopProbeAutoFallback'; the existing capability tests stay green → go test ./loop -run 'TestLoopProbe|TestLoopIncapable' -v 2>&1 | grep -q -- '--- PASS: TestLoopProbesRouteOnce'; the loop and command packages stay green with their fake executors and ACP fixtures answering the probe → go test ./loop ./cmd/batuta

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`, temp dirs through `tempDir(t)`. `cmd/batuta` must build on linux, darwin and windows. Every CLI form prints compact JSON or TSV. Release tooling (`CHANGELOG.md`, `.release-please-manifest.json`, `release-please-config.json`, `.goreleaser.yaml`, `.github/workflows/*`) is never touched.

Environment setup is never a question. `GOCACHE` is already set to a writable directory and the Go 1.26.4 toolchain is first on PATH with `GOTOOLCHAIN=local`: run `go test` plainly. If your shell still has `GOROOT` pointing to the mise Go 1.24.2 install, run `env -u GOROOT go test …`; if that is refused too, finish the code and tests, say so in your report and stop — the conductor's gate runs the suite. Run the tests named in your task's Accept lines. If the sandbox blocks a loopback listener, write the tests and say so in the report: the conductor's gate runs the whole suite.

Every proof greps the `-v` output for a named `--- PASS:` line because the binary running this loop predates the no-tests rule; each named test must exist with exactly that name.

**Task 1.** `prepare` wires `probeBackend := executor.CLIBackend{…}` and `backend := loopTransport(opts.Transport, probeBackend)` (`loop/runner.go` ~472–490); `probeRoute` (`loop/capability.go` ~60) always calls `executor.ProbeCapability` with `r.probeBackend`. A probe over ACP must not reuse the task's ACP session and must leave no session behind.

**Task 1.** Why the Scope is wider than in the first plan: the first attempt (delivery `review-contract-preflight-fixes-3-20261006-193702`, abandoned after its other five tasks integrated) stopped because the ACP test fixtures count sessions and calls, and a separate probe session changes those counts. The test files added to Scope (`loop/attempt_test.go`, `loop/settle_test.go`, `loop/finished_telemetry_test.go`, `loop/supervision_policy_test.go`, `loop/supervision_test.go`, `cmd/batuta/main_test.go`) may change only so that their fakes answer the probe or their expected sessions and calls count it; nothing else in them changes. No production flag or branch may exist only to skip the probe in tests.

**Task 1.** The first attempt's work is kept in git and may be reused after reading it, never applied blindly: `git show refs/batuta/salvage/review-contract-preflight-fixes-3/task-4-e1` (`loop/capability.go`, `loop/capability_test.go`, `loop/runner.go`, `docs/loop.md`).
