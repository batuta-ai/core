# Plan — Roadmap: phases above plans
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:8ddd757ea7e2 -->

**Goal:** Give the loop a level above the plan: `.batuta/roadmap.md` lists the phases of a delivery in order, each phase is one approved plan, `batuta loop --roadmap` runs them one after the other on the same branch, the roadmap line is ticked when a phase's plan is archived, and the journal carries roadmap and phase so the dashboard and `trail` can show them. Waves stay computed from `Depends on`, never written. Closes core #63.
**Created:** 2026-09-06 · **Status:** approved

## Tasks
- [x] 1. Parse `.batuta/roadmap.md` into phases with an optional plan slug — backend/medium
      Scope: routing/roadmap.go, routing/roadmap_test.go
      Accept: ParseRoadmap reads `# Roadmap — <title>` and `- [ ] N. <title> → plans/<slug>.md` lines into Roadmap{Title, Phases[]{Number, Title, Slug, Done}} keeping a phase without tail (Slug empty) and a ticked `- [x]` phase → go test ./routing -run TestParseRoadmapReadsPhasesInOrder -count=1; a duplicated or non-increasing phase number, a slug outside plans/, and a missing title line fail with ErrReauthoringRequired and the line number → go test ./routing -run TestParseRoadmapRejectsBrokenContractsWithTheLine -count=1; NewRoadmapLoader(root).Load reads .batuta/roadmap.md and reports a phase whose plan file is absent as Missing without failing → go test ./routing -run TestRoadmapLoaderReportsMissingPlans -count=1
- [x] 2. Archiving a plan ticks its phase in the roadmap — backend/medium
      Depends on: 1
      Scope: loop/report.go, loop/loop_test.go, routing/roadmap.go, routing/roadmap_test.go
      Accept: when the loop moves a finished plan to .batuta/plans/done/ and a roadmap names that slug, the roadmap line becomes `- [x]` and nothing else in the file changes → go test ./loop -run TestLoopTicksTheRoadmapPhaseWhenThePlanIsArchived -count=1; a workspace without a roadmap archives exactly as before → go test ./loop -run TestLoopArchivesTheFinishedPlan -count=1; routing exposes TickPhase(path, slug) that rewrites only that line → go test ./routing -run TestTickPhaseRewritesOnlyTheLine -count=1
- [x] 3. The opened record carries roadmap and phase — backend/medium
      Depends on: 1
      Scope: loop/runner.go, loop/report.go, loop/loop_test.go, loop/panel.go, loop/panel_test.go
      Accept: New writes roadmap title, phase number and phase title into the `opened` record's detail when a roadmap names the plan, and leaves the fields absent otherwise → go test ./loop -run TestLoopOpenedRecordCarriesTheRoadmapPhase -count=1; `batuta trail` and the dashboard header print `phase N · <title>` when present → go test ./loop -run 'TestTrailPrintsThePhase|TestPanelHeaderShowsThePhase' -count=1
- [ ] 4. batuta loop --roadmap runs the phases in order, one delivery per approved plan — backend/high
      Depends on: 2, 3
      Scope: loop/roadmap.go, loop/loop_test.go, loop/runner.go, cmd/batuta/main.go, cmd/batuta/main_test.go
      Accept: with two approved phases the run opens delivery 1, finishes it, archives the plan, ticks the phase, then opens delivery 2 on the new head and ends StateDone → go test ./loop -run TestRoadmapRunsApprovedPhasesInOrder -count=1; a next phase whose plan is missing or not approved ends the run with StateWaitingPlan and exit code 4 without opening a delivery → go test ./loop -run TestRoadmapStopsAtAPhaseWithoutAnApprovedPlan -count=1; a blocked delivery ends the chain with state blocked and is not resumable (recovery is a new roadmap run, which opens a new delivery for the same phase), while a waiting_input delivery ends the chain with waiting_input and --resume <delivery> --roadmap continues that same phase and then the chain → go test ./loop -run 'TestRoadmapStopsOnABlockedPhase|TestRoadmapResumeContinuesTheOpenPhase' -count=1; --dry-run --roadmap prints the chain of phases with their plans and states and runs nothing → go test ./cmd/batuta -run TestLoopRoadmapDryRunPrintsTheChain -count=1
- [ ] 5. capabilities, usage and docs describe the roadmap — docs/low
      Depends on: 4
      Scope: cmd/batuta/main.go, cmd/batuta/main_test.go, docs/loop.md, README.md
      Accept: capabilities lists roadmap and usage shows `batuta loop --roadmap [--dry-run]` → go test ./cmd/batuta -run TestCapabilitiesListsRoadmap -count=1; docs/loop.md has a `## Roadmap` section naming the file contract, the exit code 4 and the ticking rule → grep -q '^## Roadmap' docs/loop.md

## Decisions and context

Three authored levels and one computed: roadmap (the delivery) → phase (one plan, approved on its own, archived to `.batuta/plans/done/` when finished) → task (as today) → wave (computed from `Depends on`). Phases never live inside one plan: the whole plan reaches every brief, a context edit would invalidate the open delivery's digest, and the 1 MB artifact limit is close on real roadmaps. Standard library only; `cmd/batuta` builds on linux, darwin and windows.

Sandbox note for executors that run `loop/` tests: set `GOCACHE=/private/tmp/batuta-gocache` and `HOME=/private/tmp/batuta-home` for the test commands and say so in the report; the loop test fixture lives in `loop/loop_test.go` (`fakeExecutor`, `setup`, `fixture.options`).

**Task 1.** File contract, mirrored on `ParsePlan` in `routing/plan.go`: line 1 `# Roadmap — <title>`; a phase is `- [ ] N. <title>` or `- [x] N. <title>`, optionally followed by ` → plans/<slug>.md` (the slug is the plan's file stem under `.batuta/plans/` or `.batuta/plans/done/`); numbers start at 1 and increase by one; everything else is prose kept out of the struct. `Roadmap.Phases[i].Done` mirrors the checkbox. The loader resolves each slug against both directories and marks `Missing` when neither has the file; it never fails on a missing plan because a phase may be listed before it is planned.

**Task 2.** `loop/report.go` already moves the plan to `plans/done/` at the end of a delivery (look for the `done` path). Ticking rewrites the roadmap in place with the same bytes except the checkbox of the matching slug; no reformatting. `.batuta/roadmap.md` is managed state like the plans: exempt from the clean-tree preflight and from the scope check.

**Task 3.** The `opened` record is written by `New` in `loop/runner.go` with an `openedDetail` (see `loop/panel.go`); add `roadmap`, `phase` (number) and `phase_title` fields, omitted when empty, so older journals still parse. The dashboard's header line is built in `RenderPanel`.

**Task 4.** `--roadmap` is a mode of `batuta loop`, not a new subcommand: `batuta loop --roadmap` (the file is always `.batuta/roadmap.md`). The chain is driven by a small `loop.RunRoadmap(ctx, opts)` that loads the roadmap, picks the first phase not done, requires its plan to exist with `Status: approved`, runs `New` + `Run` for it, and on `StateDone` continues to the next phase; any other terminal state ends the chain with that state; a blocked delivery is terminal and never resumed (Resume keeps refusing it, as today). New state constant `StateWaitingPlan = "waiting_plan"`, exit code 4, printed like the other terminal states. `--resume <delivery>` with `--roadmap` continues the open delivery and then the chain. Each phase reuses the same branch: the next delivery opens on the head left by the previous integration.

**Task 5.** `capabilities` output is the `commands` list in `cmd/batuta/main.go`; add `roadmap` as a capability name (not a subcommand). Keep `docs/loop.md`'s style: one section, short, the file contract in a fenced block.
