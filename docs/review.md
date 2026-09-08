# `batuta review` — delivery review through the adapters

`batuta review --base <ref> [--spec <plan>]` performs a read-only review of a
delivery. It inventories the diff, divides it into bounded cohorts, runs each
cohort through an executor adapter, checks an optional plan as the delivery
specification, and derives one mechanical verdict.

```text
batuta review [--base <ref>] [--worktree] [--spec <plan>]
              [--cohort-files N] [--parallel N]
              [--reviewer <executor/model>] [--full] [--out <dir>]
```

`--base` defaults to the branch point when Batuta can determine one. Use
`--worktree` to include untracked, non-ignored files as well as tracked and
staged changes; it does not change how the default base is selected.
`--reviewer` overrides routing with an exact
`executor/model`; otherwise the optional `review` role in `.batuta/routing.md`
wins, followed by the general/high lane.

## Manifest and cohorts

The manifest records the resolved base commit and every changed path, including
status, rename source, new-side hunks, added and deleted line counts, binary and
ignore status, and whether the path was selected. Vendored dependencies,
lockfiles and generated output remain accounted for but are not selected for a
reviewer.

Selected paths are grouped deterministically in manifest order. A cohort holds
at most eight files by default (`--cohort-files` changes this) and at most 1,200
changed lines when it combines files. An oversized file is reviewed in full as
its own cohort and marked `oversized` in the manifest and walkthrough, so every
reviewer receives whole-file ownership within the delivery diff.

## Reviewer runtime

Each cohort runs in a fresh invocation of the configured executor's adapter
`readonly` command, using high reasoning and the exact routed model. The prompt
contains the project profile, its inherited convention templates, and the
cohort's zero-context hunks. `--parallel N` bounds concurrent sessions; the
default is one.

Reviewers may inspect surrounding repository code, but may report findings only
on new-side lines inside their cohort hunks. Batuta checks the worktree signature
before and after each attempt and again after the complete batch. Any write or
unattributable tree change fails the review with `exit 1` and no artefacts.
Incomplete output or an exhausted reviewer attempt leaves the affected coverage
incomplete; incomplete coverage produces `REWORK`.

## Findings contract

Every reviewer prints exactly one marker block containing one JSON object per
line, followed optionally by a plain-text coverage note:

```text
<<<FINDINGS
{"severity":"major","kind":"defect","file":"relative/path.go","line":12,"end_line":12,"premise":"Evidence for the problem","path":"Execution path","verdict":"Observable failure","fix":"Suggested fix","rule":"Applicable rule, or empty"}
FINDINGS>>>
```

An empty block means no findings. Unknown or duplicate fields, `null` values,
invalid paths or ranges, malformed framing, and findings outside the assigned
hunks are rejected. Accepted findings are deduplicated deterministically by
location and premise, retaining the higher severity, then ordered by severity
and location.

The severity taxonomy is:

- `blocker`: wrong behaviour on a plan criterion, data loss, security, or a
  build or test break.
- `major`: wrong behaviour outside a criterion, a missing test for a changed
  invariant, a resource leak, or a race.
- `minor`: clarity, dead code, duplication, or documentation drift.
- `nit`: style.

Kinds carry different evidence chains: a `defect` is
Premise → Path → Verdict; an `advisory` is
Premise → Improvement → Fix.

## Spec conformance and linter overlap

With `--spec <plan>`, Batuta loads every acceptance criterion from the named
plan and runs a separate read-only sweep over the complete diff. An explicit
path (for example, `--spec .batuta/plans/done/review.md`) loads that exact file.
A slug searches `.batuta/plans/<slug>.md`, then
`.batuta/plans/done/<slug>.md`, then the legacy `.batuta/plan-<slug>.md`.
The criteria are loaded once before reviewer sessions start. The sweep must
return each criterion in order as `satisfied`, `violated`, or `not-applicable`,
with an evidence path. A violated criterion, malformed response, or incomplete
spec coverage produces `REWORK`.

If `.batuta/profile.md` declares a `Lint:` command, Batuta runs it before the
review sessions and retains diagnostics anchored to selected new-side lines.
Reviewer findings are suppressed from the merged findings only when their ranges
overlap a linter diagnostic and both have matching, non-empty normalised rules.
The report counts these overlaps so automated and human signals are not presented
twice. Truncated lint stdout or stderr fails the
review instead of treating the retained prefix as complete diagnostics.

## Incremental rounds

Incremental state lives in `.batuta/reviews/state/<key>.json`. The key is the
sanitised branch name, with `-<spec-slug>` appended when `--spec` is supplied,
plus a stable hash of the exact branch identity and resolved spec path. Thus
branch names that sanitise identically and spec files with the same basename do
not share a checkpoint. The key is independent of the date and `--out`, so later
rounds continue from the last covered HEAD even when their report directory
changes. The saved HEAD must still be an ancestor of the current HEAD.

Incomplete cohort or spec coverage keeps the previous checkpoint (the resolved
base on the first round). Uncovered cohorts are saved as pending file lists with
hunk ranges. The next round rebuilds the diff from that checkpoint, including
pending untracked files even without `--worktree`; reverted changes disappear
from the rebuilt diff. Complete coverage advances the checkpoint and clears
pending cohorts.

Pass `--full` to ignore prior state and review from `--base` again. Missing state
starts a first round from the requested base; a missing state file therefore
starts a first round from the requested base. Invalid or non-ancestor state is
reported as an error. Dated report directories retain a `state.json` copy for
inspection, but incremental rounds read the stable state file.

## Artefacts and verdict

The default artefact directory is `.batuta/reviews/<date>-<slug>`; `--out`
selects another directory. Before writing, Batuta refuses destinations that
overlap tracked files, including paths reached through directory symlinks.
It checks the source tree again after publication, excluding only the declared
artifact files. Batuta writes each file atomically:

- `manifest.json`: the complete diff inventory and cohort assignment.
- `findings.json`: accepted, deduplicated, unsuppressed findings.
- `review.md`: the same human walkthrough printed to stdout.
- `state.json`: a copy of the covered checkpoint and any pending cohorts.

The conductor, not a reviewer, derives the verdict from accepted evidence:

- any blocker, violated criterion, or incomplete cohort/spec coverage →
  `REWORK`;
- otherwise, any major → `FIX_BEFORE_SHIP`;
- otherwise → `SHIP`.

Exit code `0` means `SHIP`, `2` means `FIX_BEFORE_SHIP`, and `3` means
`REWORK`. Setup or runtime errors that prevent a report exit with `1`.

Design distilled from `deep-review` in pedronauck/skills (https://github.com/pedronauck/skills) — cohorts, evidence discipline, taxonomy, linter overlap and the mechanical verdict; no text, scripts, state or publishing were adopted. The repository declared no licence when this was written (commit 18a0576, 2026-09-04).
