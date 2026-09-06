# Plan — Dashboard v2: panels, waves as phases, gates, live logs, keyboard
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:8ddd757ea7e2 -->

**Goal:** Replace the TSV panel of `batuta loop --dashboard --watch` with a terminal dashboard in the style of the maintainer's reference: a header, four boxed panels (execution, engine, progress with bars, current work), a task table grouped by wave with status, attempt and gates G0–G3, a live tail of the active executor's log, and keyboard navigation. Standard library only, three colours, golden-tested; `batuta watch` opens it with watch by default. The layout is designed first on a canvas (`/design` + `/impeccable critique`) and the approved mock becomes the golden reference. Closes core #64.
**Created:** 2026-09-06 · **Status:** approved

## Tasks
- [ ] 1. Executor output streams to the run log while the session runs — backend/medium
      Scope: loop/attempt.go, loop/loop_test.go, executor/run.go, executor/run_test.go
      Accept: while an executor runs, its stdout and stderr lines appear in .batuta/runs/<date>-<plan>-<task>-e<n>.out.log as they are produced, and the file ends with the same header and body as today when the session ends → go test ./loop -run TestLoopStreamsExecutorOutputToTheRunLog -count=1; the existing progress observer keeps working on both streams → go test ./executor -run 'TestSubprocess.*Progress' -count=1
- [ ] 2. A view model summarises the journal for the dashboard — backend/high
      Scope: loop/panel_model.go, loop/panel_model_test.go, loop/panel.go
      Accept: PanelModel(records, now, logTail) returns Header{Delivery, Branch, Head, Started, Elapsed, Status, Roadmap, Phase}, Engine{Executor, Model, Reasoning, TestCommand}, Progress{WavesDone, WavesTotal, TasksDone, TasksTotal}, Current{Wave, Task, Criterion, LastRecord, LastError, Health}, Waves[]{Number, Base, Integrated, Rows[]{Task, Title, Status, Attempt, Gates[4]}} and Logs[] from a journal with an integrated wave, a running task with BATUTA-PROGRESS records, a retried task and a blocked task → go test ./loop -run TestPanelModelSummarisesTheJournal -count=1; gates read the latest attempt's report (G0 finished, G1 tree, G2 tests, G3 verify) as pass, fail, silent or pending → go test ./loop -run TestPanelModelGateColumns -count=1; RenderPanel keeps printing the current TSV from the model until task 3 replaces it → go test ./loop -run TestPanel -count=1
- [ ] 3. The renderer draws boxed panels, bars and the wave table at the terminal width — backend/high
      Depends on: 2
      Scope: loop/panel_render.go, loop/panel_render_test.go, loop/panel.go, loop/termsize_unix.go, loop/termsize_windows.go, loop/testdata/panel-80.txt, loop/testdata/panel-120.txt
      Accept: Render(model, width, colour) draws the header, the four panels in two columns, the progress bars and the wave-grouped table with box-drawing borders and truncation to width, byte-identical to the goldens at 80 and 120 columns with colour off → go test ./loop -run TestRenderMatchesGoldens -count=1; colour on uses exactly three SGR colours (ok, error, active) and NO_COLOR=1 or a non-TTY writer disables them → go test ./loop -run TestRenderColours -count=1; the terminal width comes from a per-platform TerminalSize with a 120-column fallback and cmd/batuta builds for linux, darwin and windows → GOOS=windows go build ./... && GOOS=linux go build ./...
- [ ] 4. The logs panel tails the active execution's run log — backend/medium
      Depends on: 1, 3
      Scope: loop/panel.go, loop/panel_model.go, loop/panel_test.go
      Accept: Watch reads the last lines of the running task's .out.log on every redraw and the logs panel shows them, at most the lines that fit the remaining height → go test ./loop -run TestWatchShowsTheRunLogTail -count=1; with no running task the panel shows the last integrated task's final lines → go test ./loop -run TestWatchLogPanelAfterIntegration -count=1
- [ ] 5. Keyboard navigation with a no-TTY fallback — backend/high
      Depends on: 3
      Scope: loop/panel.go, loop/panel_keys.go, loop/panel_keys_test.go, loop/rawmode_unix.go, loop/rawmode_windows.go, cmd/batuta/main.go
      Accept: with a TTY, q ends the watch, ↑/↓ and PgUp/PgDn scroll the table, f toggles following the active task; the raw mode is restored on exit and on cancellation → go test ./loop -run TestPanelKeysScrollAndQuit -count=1; when stdin is not a TTY the watch auto-follows the active task and never blocks on input → go test ./loop -run TestWatchWithoutATTYAutoFollows -count=1; cmd/batuta builds for windows → GOOS=windows go build ./...
- [ ] 6. batuta watch opens the live dashboard by default — backend/medium
      Depends on: 4, 5
      Scope: cmd/batuta/main.go, cmd/batuta/main_test.go
      Accept: `batuta watch [<delivery>] [--interval 2s]` runs the live dashboard on the most recent open delivery and exits 0 when it ends, and `batuta watch --once` prints one snapshot and exits → go test ./cmd/batuta -run 'TestWatchOpensTheLiveDashboard|TestWatchOncePrintsASnapshot' -count=1; `batuta loop --dashboard [--watch]` keeps working and capabilities lists watch → go test ./cmd/batuta -run 'TestLoopDashboardStillWorks|TestCapabilitiesListsWatch' -count=1
- [ ] 7. Docs and help describe the dashboard — docs/medium
      Depends on: 6
      Scope: docs/loop.md, cmd/batuta/main.go, README.md
      Accept: docs/loop.md replaces the dashboard paragraph with the panel layout, the gate columns, the keys, the no-TTY behaviour and `batuta watch` → grep -q 'batuta watch' docs/loop.md; usage names `watch`, --once, --interval and the keys → go test ./cmd/batuta -run TestUsageMentionsTheDashboardKeys -count=1

## Decisions and context

Visual reference: the maintainer's "LOOP ENGINEER" terminal (2026-09-06): title, two columns of boxed panels ("Execução", "Engine", "Progresso", "Trabalho atual"), a table with ID · Phase/Task · Status · Attempt · Gates, a status line with scroll hints, and a "Logs recentes" box. Batuta's version keeps the layout, drops the figlet banner (plain `batuta loop · <delivery>`), and uses waves as the phase rows: a wave is the dependency-safe group the loop opens, executes, integrates and closes, so it has a number, a base commit and an integrated head. Retries, escalations and conflict re-executions belong to the task's Attempt column (`e2/4`, `⏫ terra`), never to a new wave row. Labels in Portuguese as in the reference (Execução, Engine, Progresso, Trabalho atual, Logs recentes, Tentativa, Gates); values as the journal has them.

Standard library only: no bubbletea, no x/term. Terminal size and raw mode come from platform files with build tags (`_unix.go` using `syscall` ioctl and termios, `_windows.go` using the console API through `syscall`); `cmd/batuta` must keep building on linux, darwin and windows. Three colours only (ok, error, active), `NO_COLOR` honoured, colour off when the writer is not a TTY. Golden tests fix width and colour so output is byte-comparable.

Sandbox note for executors that run `loop/` tests: set `GOCACHE=/private/tmp/batuta-gocache` and `HOME=/private/tmp/batuta-home` for the test commands and say so in the report; the loop test fixture lives in `loop/loop_test.go` (`fakeExecutor`, `setup`, `fixture.options`).

**Task 1.** `executor.Subprocess.Execute` already tees stdout and stderr through `progressObserver`s; the run log is written afterwards by `writeLog` in `loop/attempt.go`. Streaming means the loop opens the log file before `Execute` and passes writers that append lines as they arrive; the final `writeLog` may rewrite the file with the header so the format stays the one documented in `docs/loop.md`.

**Task 2.** The journal is the only source: `KindOpened` (slug, branch, head, and after the roadmap plan: roadmap, phase), attempt records with the gate report, `task_progress` records (criterion START/DONE), `KindSettled` (wave, final_head, conflict_task), `limit_wait`, terminal records. Health signals: journal age, tree dirty (from the last gate 1 signature when present), last error text. Roadmap and phase fields are optional; the model shows them only when present.

**Task 3.** The approved mock lives in `docs/dashboard-mock.txt` (80 and 120 columns), produced from the design canvas before this plan runs; the goldens must match it. Layout at 120 columns: header line; panels row 1 "Execução" | "Engine", row 2 "Progresso" | "Trabalho atual"; then the table; then the status line (`▲ n acima · ▼ n abaixo · f segue a task · q sai`); then "Logs recentes". At 80 columns the panels stack in one column. Box drawing with `┌─┐│└┘` and a title in the top border. Progress bar: `[████░░░░]` plus `n/m` and a percentage. Truncate with `…`.

**Task 6.** `watch` is a thin alias in `run`'s dispatcher: it parses `--interval`, `--once` and an optional delivery, then calls `loop.Watch` (or `loop.Dashboard` with `--once`); the three-edit rule for a new subcommand applies (case in `run`, handler, name in `commands` and `usage`).

**Task 5.** Raw mode only for the duration of the watch; every exit path restores the terminal (deferred, and on `ctx.Done()`). Key reading runs in its own goroutine feeding a channel; the redraw loop selects on the ticker, the keys and the context. Windows: `SetConsoleMode` through `syscall.NewLazyDLL("kernel32.dll")` is acceptable inside the `_windows.go` file.
