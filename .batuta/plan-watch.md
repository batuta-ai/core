# Plan — live dashboard and per-criterion progress (core #34)
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:8ddd757ea7e2 -->

**Goal:** `batuta loop --dashboard --watch` redraws a live panel of the open delivery, and the loop sees inside an executor session through a text protocol: the brief asks for `BATUTA-PROGRESS <n> START|DONE` lines per acceptance criterion, `executor.Subprocess` streams stdout line by line, the loop journals `task_progress` records, gate 3 names criteria reported DONE whose proof failed.
**Created:** 2026-09-06 · **Status:** approved

## Tasks
- [x] 1. Tee the child's stdout to an observer in the publication runner — backend/medium → codex/gpt-5.6-sol
      Scope: publication/command.go, publication/command_test.go
      Accept: `Command` gains an optional `Observer io.Writer` and `ExecRunner.Run` writes every stdout chunk to it while the bounded buffer still keeps the output → go test ./publication -run TestExecRunnerTeesStdoutToObserver; existing runner behavior unchanged when Observer is nil → go test ./publication; whole module builds → go build ./...
- [x] 2. Parse BATUTA-PROGRESS lines while the executor runs — backend/medium → codex/gpt-5.6-sol
      Depends on: 1
      Scope: executor/run.go, executor/progress.go, executor/progress_test.go, executor/adapter_test.go
      Accept: `executor.ParseProgress(line)` returns (criterion int, state START|DONE, ok) only for a whole line of the form `BATUTA-PROGRESS <n> START` or `BATUTA-PROGRESS <n> DONE` and rejects everything else → go test ./executor -run TestParseProgress; `Subprocess` gains `Progress func(ProgressEvent)` and `Execute` feeds it one event per protocol line as the child prints it, through the runner's Observer, and appends the same events to `Result.Progress` → go test ./executor -run TestSubprocessStreamsProgressEvents; a partial last line without newline is still parsed at exit → go test ./executor -run TestSubprocessStreamsProgressEvents; nothing changes for output without protocol lines → go test ./executor
- [ ] 3. Journal task_progress records and put the protocol in the brief — backend/high → codex/gpt-6-astra
      Depends on: 2
      Scope: loop/attempt.go, loop/brief.go, loop/runner.go, loop/brief_test.go, loop/loop_test.go
      Accept: the brief carries a "## Progress protocol" section with the two line shapes verbatim → go test ./loop -run TestBriefCarriesTheProgressProtocol; the loop journals one `task_progress` record per event with detail {execution, criterion, state} while the executor is still running → go test ./loop -run TestLoopJournalsProgressWhileTheExecutorRuns; the fake executor's default scenario prints START and DONE for each criterion and the three-task delivery still integrates → go test ./loop -run TestLoopDeliversAThreeTaskPlanWithADependency; the journal chain verifies and --resume ignores task_progress records → go test ./loop -run 'TestLoopResumesAnExecutorKilledMidRun|TestLoopResumesAfterAStopBetweenWaves'; the whole suite passes → go test ./...
- [ ] 4. Gate 3 names a criterion reported DONE whose proof failed — backend/medium → codex/gpt-5.6-sol
      Depends on: 3
      Scope: loop/attempt.go, loop/loop_test.go
      Accept: when the executor printed `BATUTA-PROGRESS n DONE` and proof n fails, the failure feedback contains a line naming criterion n as reported done but failing its proof → go test ./loop -run TestGateThreeNamesACriterionReportedDoneButFailing; feedback unchanged when no progress lines were printed → go test ./loop -run TestLoopRetriesInTheSameWorktreeThenSucceeds; whole suite → go test ./...
- [ ] 5. Render the live panel from a delivery's journal — backend/high → codex/gpt-6-astra
      Depends on: 3
      Scope: loop/panel.go, loop/panel_test.go, loop/report.go
      Accept: `loop.RenderPanel(records, now) string` prints the header line (delivery, branch @ head, wave x/y, elapsed), one row per task (task, lane, executor/model, exec, state, detail) and a `last` line with the latest record summary, in the layout given in Decisions → go test ./loop -run TestRenderPanelLayout; a running task's detail shows elapsed time and `d/t items` from task_progress records → go test ./loop -run TestRenderPanelShowsCriterionProgress; the pending row names what it waits on and the integrated row its commit → go test ./loop -run TestRenderPanelLayout; `Dashboard` TSV output is byte-for-byte unchanged → go test ./loop -run TestDashboardTSVUnchanged
- [ ] 6. batuta loop --dashboard --watch redraws the panel until the delivery ends — backend/medium → codex/gpt-5.6-sol
      Depends on: 5
      Scope: cmd/batuta/main.go, cmd/batuta/main_test.go, loop/panel.go, loop/panel_test.go
      Accept: `loop.Watch(ctx, workspace, delivery, interval, w)` clears the screen and reprints the panel every interval and returns when the journal reaches a terminal state or ctx ends → go test ./loop -run TestWatchStopsAtTerminalState; `batuta loop --dashboard --watch [--interval 2s] [<delivery>]` wires it and `--dashboard` alone still prints the TSV → go test ./cmd/batuta -run TestRunLoopDashboard; usage documents --watch and --interval → go run ./cmd/batuta help | grep -q -- '--watch'; capabilities unchanged → go test ./cmd/batuta
- [ ] 7. Document the protocol, the record and the panel — docs/low → codex/gpt-5.4-mini
      Depends on: 6
      Scope: docs/loop.md
      Accept: docs/loop.md describes the BATUTA-PROGRESS protocol, the task_progress journal record and --dashboard --watch → grep -q 'BATUTA-PROGRESS' docs/loop.md && grep -q 'task_progress' docs/loop.md && grep -q -- '--watch' docs/loop.md; no other file changed → test "$(git diff --name-only HEAD | grep -vc '^docs/loop.md$')" = 0

## Decisions and context

Issue: https://github.com/batuta-ai/core/issues/34. Repository map: `.batuta/profile.md`. The loop runs this plan itself, so nothing here may route to `self`.

**Progress protocol (exact text for the brief, task 3).** The brief section is titled `## Progress protocol` and says: for each acceptance criterion n, print an isolated line `BATUTA-PROGRESS <n> START` before the first edit toward it and `BATUTA-PROGRESS <n> DONE` when its proof passes locally. Same shape as the `BATUTA-QUESTION:` line: plain text on stdout, nothing else on that line, no tool required. Criterion numbers are the 1-based positions of the `## Acceptance criteria` list.

**Streaming (tasks 1 and 2).** `publication.Command` gets one optional field, `Observer io.Writer`; `ExecRunner.Run` wraps stdout in an `io.MultiWriter(bounded, observer)` when set. Nothing else in `publication` changes; the bounded buffer stays the source of `CommandResult.Stdout`. In `executor`, a small line splitter (`bufio`-style, handling a trailing partial line at exit) sits behind the Observer and calls `ParseProgress`; a match produces `ProgressEvent{Criterion int, State string, At time.Time}` delivered to `Subprocess.Progress` synchronously and appended to `Result.Progress`. `ParseProgress` accepts exactly `BATUTA-PROGRESS <n> START|DONE` after trimming spaces, n ≥ 1; anything else, including `BATUTA-PROGRESS` inside a longer line, is not an event. Timeouts, limits and the question line keep their current behavior.

**Journal (task 3).** New kind `KindProgress journal.Kind = "task_progress"` next to the other kinds in `loop/runner.go`; detail `{"execution": e, "criterion": n, "state": "START"|"DONE"}` (the record's own `at` is the timestamp); written through `r.locked` from the Progress callback set on the runner's `Subprocess` per attempt, so records interleave with `executor_started`/`executor_finished` in order and carry the graph like every record. `replay` in `loop/settle.go` already ignores unknown kinds: task 3 adds a test that proves resume after a kill still works with progress records in the file, and changes `settle.go` only if that test fails. `recordSummary` in `loop/report.go` may add a case for the new kind (`criterion`, `state`) so `batuta trail` reads well — that is the only change allowed in `report.go` for task 3, and only if Scope is widened; otherwise leave it to task 5.

**Fake executor (task 3).** The mock engine in `loop/loop_test.go` (`fakeExecutor`) counts the criteria in the brief and prints `BATUTA-PROGRESS i START` then `BATUTA-PROGRESS i DONE` for each before writing its files, in the default scenario only; other scenarios stay as they are. The Progress callback in tests may also be observed directly through `Options` if a hook is needed — prefer asserting on journal records.

**Gate 3 cross-check (task 4).** After `gates.Proofs`, for each criterion n with a failing proof and a DONE event in `Result.Progress`, append the feedback line `criterion n was reported DONE but its proof failed: <proof command>` to the failure feedback (the list `report.Failures()` returns, or alongside it in `recordFailure`). No change to the verdicts themselves; the gate result is unchanged, the feedback is richer.

**Panel (tasks 5 and 6).** `RenderPanel` is a pure function over `[]journal.Record` and a `now`; it reads the graph from the last record like `Dashboard` does. Layout, one delivery:

```
delivery greetings-20260906-030001   branch main @ 4e2651c   wave 2/2   elapsed 04:12
task     lane            executor/model   exec  state        detail
task_1   backend/low     codex/fake-low   1     integrated   commit 440c12b
task_2   backend/low     codex/fake-low   2     running      02:31 · 2/4 items · gate —
task_3   backend/medium  codex/fake-mid   -     pending      after task_1, task_2
last     gates_reported task_2 e1 passed=false (scope: outside.txt)
```

Columns are aligned with `text/tabwriter` like the TSV. `elapsed` counts from `delivery_opened`; a running task's detail counts from its `executor_started`; `d/t items` = criteria with a DONE record over the task's criteria count (from the graph's task, or from the plan when the graph does not carry it — then print `d items`); `gate —` until a `gates_reported` record exists for that execution, then `gate ok` or `gate fail`. `wave x/y`: waves admitted so far over the dry-run's planned wave count when the journal carries it, otherwise `wave x`. `Watch` prints `\x1b[2J\x1b[H` then the panel every interval (default 2s, flag `--interval`), stops with exit 0 at a terminal record or when the context is canceled (Ctrl-C), and never writes to the journal. `--dashboard` without `--watch` keeps today's TSV; there is no `--once` flag. With no delivery argument, `--watch` follows the most recent open delivery and says `no open deliveries` and exits 0 when there is none.

**Conventions that matter here.** Standard library only. Tests table-driven, `t.Parallel()` where the test allows; git fixtures set `user.name`, `user.email`, `commit.gpgsign false`; temp dirs resolved through `filepath.EvalSymlinks`. `cmd/batuta` must build with `GOOS=windows`. Do not touch `CHANGELOG.md`, release-please files, `.goreleaser.yaml`, `.github/`. The signatures `NewArtifactLoader`, `BuildCandidateBindings`, `Selector.Select`, `RecordFailure`, `SettleWave` are frozen. Skills-side documentation (`batuta-loop` SKILL.md, `references/brief.md`) is a separate repository and a follow-up after this plan.
