# Profile — core

Written by /batuta-init on 2026-09-06. Complements README.md and docs/loop.md; never repeats them.

Stack: Go 1.26 (module github.com/batuta-ai/core, standard library only)
Methodology: TDD; conventional commits; feature branches with a PR to main (release-please, prerelease versioning)
Test: go test ./...
Build: go build ./...
Install:
Execution: sequential
Worktree: always
Template: templates/generic.md

## Conventions

- One PR per change; `feat:` and `fix:` drive the release, `chore:` never does.
- Table-driven tests with `t.Parallel()`; temp dirs through `tempDir(t)` (symlink-resolved on macOS).
- `cmd/batuta` must build on every goreleaser target (linux, darwin, windows): no bare `syscall` in packages it imports; platform files (`_unix.go`, `_windows.go`) with build tags.
- Public signatures called by batuta-ai/compozy are frozen: `NewArtifactLoader`, `BuildCandidateBindings`, `Selector.Select`, `RecordFailure`, `SettleWave`.
- Every CLI subcommand prints compact JSON or TSV; exit 0 pass, 2 fail, 1 error.

## Project map

Swept by the research lane (agy / gemini-3.8-flash-low) on 2026-09-06; report in `.batuta/scout/2026-09-06-project-map.md`.

Go module with no third-party dependencies. Every package sits at the root; tests live beside the code as `*_test.go`.

- `cmd/batuta/main.go` — the CLI. `run` dispatches on `args[0]` (`version`, `capabilities`, `inventory`, `doctor`, `loop`, `trail`). A new subcommand needs three edits in this file: a `case` in `run`, a `run<Name>` handler, and its name in `var commands` (line 119, what `capabilities` prints) plus the `usage` string. `main_test.go` exercises the dispatcher.
- `routing/` — delivery graph and routing. `graph.go` (`DeliveryGraph`, waves, task states), `delivery.go` (invariants), `table.go` (`ParseRoutingTable`, model matching, `RoutingTable.Generation`), `plan.go` (`PlanLoader`, `.batuta/plan-*.md`), failure policy. Signatures used by batuta-ai/compozy are frozen (see Conventions).
- `inventory/` — discovery of executor CLIs: `types.go` (executor IDs, snapshot), `collect.go` and `adapters/` (one probe per CLI: `agy.go`, `claude.go`, `codex.go`, `compozy.go`, `cursor.go`, `opencode.go`), `redact.go`.
- `integration/` — takes a verified worktree candidate into the canonical branch: `service.go` (`Service.Integrate`), `git.go` (preflight, diff, cherry-pick, commit).
- `publication/` — bounded subprocess runner (`command.go`), git tree inspection and snapshots (`WorktreeState`), publication plans (`publish.go`).
- `repository/bootstrap.go` — guarded repository bootstrap.
- `journal/journal.go` — append-only hash-chained JSONL under `.batuta/journal/`; the loop resumes from it.
- `worktree/git.go` — worktree allocation under `.batuta/worktrees/`, squash, exclude handling; platform-specific lock in `_unix.go` / `_windows.go` files.
- `executor/` — adapter frontmatter parser and argv builder (`adapter.go`), shell-less subprocess runner (`run.go`, `Subprocess`). Adapters are read from the skills tree (`~/.agents/skills/batuta/adapters/`).
- `gates/gates.go` — the four gates: `Finished`, `Tree`, `Tests`, and gate 3 as `Scope`, `Proofs`, `Verifier`; `Report.Decide` gives the verdict.
- `loop/` — the mechanical conductor: `runner.go` (`Runner.Run`, `New`, `Resume`), `attempt.go` (`runAttempt` runs the executor, then the gates at line ~191), `settle.go` (wave settlement, crash recovery), `brief.go` (executor brief rendering, profile lines), `profile.go` (reads this file), `report.go` (terminal report, WORK.md bookkeeping, `--dashboard` TSV).
- Tests: table-driven, `t.Parallel()`. Each package carries its own `tempDir(t)` helper (`routing/`, `integration/`, `publication/`, `inventory/`) resolving macOS symlinks. `loop/loop_test.go` holds `fakeExecutor`, a shell script mock engine driven by `FAKE_SCENARIO`; it is the place to add loop scenarios. `go test ./...` runs in about 20 s.
- Do not touch: `CHANGELOG.md`, `.release-please-manifest.json`, `release-please-config.json`, `.goreleaser.yaml`, `.github/workflows/*` — release tooling, edited only by a release task.
- Design notes for the loop, its invariants and file-system contract: `docs/loop.md`.
