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
staged changes. `--reviewer` overrides routing with an exact
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
changed lines. A single file over that line limit is rejected rather than split,
so every reviewer receives whole-file ownership within the delivery diff.

## Reviewer runtime

Each cohort runs in a fresh invocation of the configured executor's adapter
`readonly` command, using high reasoning and the exact routed model. The prompt
contains the project profile, its inherited convention templates, and the
cohort's zero-context hunks. `--parallel N` bounds concurrent sessions; the
default is one.

Reviewers may inspect surrounding repository code, but may report findings only
on new-side lines inside their cohort hunks. Batuta checks the worktree signature
before and after each attempt and again after the complete batch. Any write,
unattributable tree change, incomplete output, or exhausted reviewer attempt
leaves the affected coverage incomplete; incomplete coverage produces `REWORK`.

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
plan and runs a separate read-only sweep over the complete diff. The sweep must
return each criterion in order as `satisfied`, `violated`, or `not-applicable`,
with an evidence path. A violated criterion, malformed response, or incomplete
spec coverage produces `REWORK`.

If `.batuta/profile.md` declares a `Lint:` command, Batuta runs it before the
review sessions and retains diagnostics anchored to selected new-side lines.
Reviewer findings whose ranges overlap a linter diagnostic are suppressed from
the merged findings; the report counts these overlaps so automated and human
signals are not presented twice.

## Incremental rounds

The output directory's `state.json` records the reviewed HEAD. A later review
using the same directory starts from that commit when it is still an ancestor
of the current HEAD, so the next round covers only new changes. Pass `--full`
to ignore prior state and review from `--base` again. Missing, invalid, or
non-ancestor state is reported instead of silently widening or narrowing the
review.

## Artefacts and verdict

The default artefact directory is `.batuta/reviews/<date>-<slug>`; `--out`
selects another directory. Batuta writes atomically:

- `manifest.json`: the complete diff inventory and cohort assignment.
- `findings.json`: accepted, deduplicated, unsuppressed findings.
- `review.md`: the same human walkthrough printed to stdout.
- `state.json`: the reviewed HEAD used by incremental rounds.

The conductor, not a reviewer, derives the verdict from accepted evidence:

- any blocker, violated criterion, or incomplete cohort/spec coverage →
  `REWORK`;
- otherwise, any major → `FIX_BEFORE_SHIP`;
- otherwise → `SHIP`.

Exit code `0` means `SHIP`, `2` means `FIX_BEFORE_SHIP`, and `3` means
`REWORK`. Setup or runtime errors that prevent a report exit with `1`.

Design distilled from `deep-review` in pedronauck/skills (https://github.com/pedronauck/skills) — cohorts, evidence discipline, taxonomy, linter overlap and the mechanical verdict; no text, scripts, state or publishing were adopted. The repository declared no licence when this was written (commit 18a0576, 2026-09-04).
