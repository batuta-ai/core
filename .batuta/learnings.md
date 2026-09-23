
## 2026-09-06 — loop runs on core (roadmap, dashboard)

- A docs task's criteria must protect the existing content: `grep -c '^## ' docs/loop.md` not below its previous count, or a named section still present. Task 5 of `roadmap` (gpt-5.4-mini) replaced `docs/loop.md` with a ten-line stub and passed a proof that only grepped one heading.
- Never join two behaviours with different outcomes in one criterion ("a blocked or waiting_input delivery … and --resume continues"): the read-only verifier reads it literally and vetoes the honest implementation. One criterion per outcome.
- Sandbox recipe for Go on this machine (codex `--sandbox workspace-write`): `HOME=/private/tmp/batuta-home GOCACHE=/private/tmp/batuta-gocache GOPATH=/Volumes/Home/francisross/go GOMODCACHE=/Volumes/Home/francisross/go/pkg/mod`; `GOTOOLCHAIN` stays `auto` (go1.26.4 is cached in the module cache; the PATH go is 1.24.2). Write it in the plan's shared Decisions with "environment setup is never a question" — gpt-6-astra otherwise spends executions asking (dashboard task 2: three questions, delivery abandoned).

## 2026-09-08 — hardening roadmap, seven review rounds

- Executors never see excluded artefacts (`.batuta/reviews/`, `.batuta/runs/`): everything a task needs goes verbatim into the plan's `**Task N.**` paragraph.
- A parked question does not end a run while independent tasks remain: never `--abandon` or `--answer` while `pgrep` shows the runner; identify the runner by its delivery, not by pid order. Task 9 of plan `loop-deadends` now refuses those commands on a live lock.
- Changing a plan's Scope while its delivery is open means `--abandon` + new delivery; answer an executor's scope question by editing the plan, not with `--answer`.
- Run the loop with a binary built from the branch when the branch fixes the loop itself (`go build -o /private/tmp/batuta-hardening ./cmd/batuta`); the installed release repeated #70 and discarded a task's work.
- On this 16 GB machine with a VM resident, `go test -race ./...` and parallel codex sessions starve each other: the loop package "hung" for 8 min and two tests failed only under that load. Verify one thing at a time; leave the full `-race` suite to CI (`-timeout 30m`).
- Never let a verification chain continue past a `FAIL` line: use `set -o pipefail` or check the exit of `go test` itself, not of the `grep` after it (the round-5 merge slipped through that way).
- `batuta review` converges on a fresh delivery (17 → 8 findings) but a fix in a delicate area (`finish`, presence, `--answer`) yields one or two new findings per round; after three incremental rounds the conductor brings the ship/fix decision to the maintainer instead of looping on.
- Submit keys in a TUI editor must be ones every terminal delivers: Enter (send), `ctrl+j` (newline); `ctrl+s`, `alt+enter`, `ctrl+enter` depend on the terminal.
- After the maintainer chose the surgical cycle over shipping with issues (2026-09-08 17:00 UTC): one codex gpt-6-astra cycle on `loop/report.go`/`loop/presence.go` (8 min, 80k tokens) closed the round-8 pair and round 9 returned SHIP with 0 findings. The pattern holds: the review converges once the fix is confined to the reviewed lines and factors shared logic (`presenceInspectionError`) instead of duplicating it.
- release-please's release PRs (core #76, host #75) were merged by the maintainer's account within a minute of creation on 2026-09-08: assume auto-merge is on and do not wait for a manual merge step.

## 2026-09-08 evening — installer hardening, review runs the proofs

- `nohup codex exec … &` launched from a background shell must redirect `< /dev/null`: without it the session hangs forever at `Reading additional input from stdin...` and burns the wall clock (22 minutes lost tonight).
- Never run `git worktree remove` with the shell's cwd inside that worktree, and run `git merge --ff-only` from the main checkout, not from the worktree of the branch being merged: the whole command chain dies with `Unable to read current working directory`. The commit survives on its branch; recover by merging from the checkout.
- A path named only in a task's `Accept` proof must also appear in its `Scope`: the scope gate rejected task 2 of `installer-hardening` for writing `tests/pin-core.test.sh`. Fixing the plan means abandoning the open delivery and opening a new one.
- `batuta review` aborts with `source tree changed during review` for any tree mutation while it runs, including an edit to `.git/info/exclude`. Do nothing in the repository while a review is running.
- `gpt-5.4-mini` is no longer served to the ChatGPT account (`The 'gpt-5.4-mini' model is not supported when using Codex with a ChatGPT account.`). The `low` lane moved to `agy gemini-3.8-flash-low`, which took its first loop task (the commit-subject fix) on the first attempt.
- A review round whose findings all sit in freshly written code converges: three rounds on the installer branch went 5 → 4 → 1, and the `review-proofs` branch went 1 → 0 after one surgical cycle.

- 2026-09-17 ACP review: preserve sticky process-discovery uncertainty. A later successful snapshot cannot reconstruct a missed observation interval; clearing the latch changes the approved fail-closed contract. Reject recommendations to relax it solely to improve availability.

- 2026-09-18 ACP review: do not accept unresolved shutdown as a passing native integration outcome or remove cooperative-exit assertions to hide timing failures. Investigate the lifecycle cause; tuning sampling/grace periods must account for escape-observation coverage and total shutdown bounds.

- 2026-09-20 judge-vercel-auto: two scope violations in one plan, both mine. A task's Scope must list every file that holds a symbol the criteria touch (the reasons list lived in `judge/judge.go`, the probe output in `cmd/batuta/judge.go`); grep the symbols before writing Scope instead of naming files from memory. The loop never widens a Scope, so the only recovery is abandon, fix the plan, relaunch.

## 2026-09-21 — claim_evidence v2.2/v2.3

- A task that changes an extractor must list in Scope every test file that uses its fixtures: grep the function name in `*_test.go` before writing Scope.
- Worktree executors cannot read `.batuta/journal/` or `.batuta/runs/`, so replay and benchmark steps belong to the conductor, never to a loop task.

## 2026-09-22 — executor telemetry

- A `limit_regex` alternative as broad as `rate limit` matches an executor's own report and makes the loop wait and re-run a finished attempt. Match provider error shapes, never prose (skills#60; the run log is kept as evidence).
- `git gc` prunes candidate commits about two weeks after the loop deletes their refs; that cost 41 corpus attempts their diffs on 2026-09-22. Keep candidate commits reachable (for example `refs/batuta/corpus/*`) when a corpus must stay rebuildable.
- ACP cache semantics differ by source: additive for the codex bridge and for claude, subset for `codex exec --json`. Never sum a total without knowing which.
- 2026-09-22 structured-output and verifier-coverage: both plans stopped once on a Scope that missed a test asserting the old behaviour the task changes on purpose (`cmd/batuta/main_test.go`, `loop/attempt_test.go`). Before writing Scope, grep the tests for the behaviour being changed, not only for the symbols.
- 2026-09-22: a conductor-added "while you are there" fix (pending-line cap in fixes-3) introduced the regression the next review caught. Keep fix plans to the findings.
