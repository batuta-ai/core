# Judge benchmark — claim versus evidence, retroactive replay

Date: 2026-09-20 (replayed 2026-09-21 00:40 local). Binary: `cmd/batuta` built from `366c681` (branch `feat/judge-claim-evidence`). Judge: `provider: auto` → TypeSafe direct (`jev-1.13.0`), decision `claim_evidence` in `shadow`, threshold 0.9. Command per journal: `batuta judge replay --journal <path> [--runs <dir>]`. Every number below is copied from the command output; nothing is rounded or averaged by hand.

## Raw results (23 finished attempts, 9 deliveries)

| repo | delivery | attempt | recorded outcome | claim_unsupported | verifier_contradicted |
|---|---|---|---|---|---|
| core | research-ladder-20260920-001419 | task_1 e1 | candidate | 0.63 | 0.14 |
| core | research-ladder-20260920-001419 | task_3 e1 | **already_satisfied (false, see PR #96)** | 0.76 | 0.08 |
| core | research-ladder-20260920-001419 | task_2 e2 | candidate | 0.83 | 0.85 |
| core | silent-proofs-20260920-105706 | task_1 e1 | candidate | 0.60 | 0.13 |
| core | cursor-models-ansi-20260920-112641 | task_1 e1 | candidate | 0.35 | 0.08 |
| core | judge-package-20260920-124734 | task_1 e1 | candidate | 0.36 | 0.04 |
| core | judge-package-20260920-124734 | task_2 e1 | candidate | 0.40 | 0.09 |
| core | judge-package-20260920-124734 | task_3 e1 | candidate | 0.30 | 0.06 |
| core | judge-package-20260920-124734 | task_4 e1 | candidate | 0.40 | 0.10 |
| core | judge-vercel-auto-20260920-142856 | task_1 e1 | scope_violation | 0.67 | 0.49 |
| core | judge-vercel-auto-20260920-144937 | task_1 e1 | candidate | 0.47 | 0.21 |
| core | judge-vercel-auto-20260920-144937 | task_2 e1 | scope_violation | 0.32 | 0.57 |
| core | judge-vercel-auto-20260920-152219 | task_2 e2 | candidate | 0.43 | 0.05 |
| core | judge-claim-evidence-20260920-235929 | task_1 e1 | candidate | 0.32 | 0.05 |
| core | judge-claim-evidence-20260920-235929 | task_2 e1 | candidate | 0.52 | 0.06 |
| core | judge-claim-evidence-20260920-235929 | task_3 e1 | candidate | 0.39 | 0.09 |
| skills | research-ladder-20260920-001419 | task_1 e1 | tests_failed | 0.65 | 0.72 |
| skills | research-ladder-20260920-001419 | task_1 e2 | candidate | 0.53 | 0.14 |
| skills | research-ladder-20260920-001419 | task_2 e1 | candidate | 0.43 | 0.08 |
| skills | research-ladder-20260920-001419 | task_3 e1 | candidate | 0.47 | 0.04 |
| skills | research-ladder-20260920-001419 | task_4 e1 | **already_satisfied (false, see skills PR #55)** | 0.66 | 0.05 |
| skills | hidden-scope-rule-20260920-105745 | task_1 e1 | tests_failed | 0.66 | 0.81 |
| skills | hidden-scope-rule-20260920-105745 | task_1 e2 | candidate | 0.62 | 0.12 |

Ground truth: the two `already_satisfied` rows are the attempts where the executor (agy) wrote nothing and claimed success; every `candidate` row was integrated and its full suite passed in its worktree; `tests_failed` and `scope_violation` rows were real gate failures with retries.

## Reading, without interpretation beyond the numbers

- Ordered by `claim_unsupported`, the two false closures sit 2nd (0.76) and 4th (0.66) of 23. Above them: `research-ladder task_2 e2` (0.83, a legitimate candidate whose executor report explicitly said criterion 6 could not be verified in its sandbox) and, tied at 0.67/0.66, one scope violation and one test failure.
- Legitimate candidates range 0.30–0.83; 17 candidates, 12 of them at or below 0.53.
- At the configured threshold 0.9, `enforce` would have fired on nothing: 0 true positives, 0 false positives.
- At 0.7: 1 true positive (core task_3), 1 false negative (skills task_4), 1 false positive (task_2 e2).
- At 0.65: 2 true positives, 3 false positives (task_2 e2 and two attempts that the gates had already failed on their own).
- `verifier_contradicted` is high (0.49–0.85) exactly on attempts whose gates failed (tests or scope) and on the sandbox-limited task_2 e2; it is low (≤0.14) on both false closures. It does not detect the false closures; it mostly restates gate failures.
- Cost: 23 calls, all answered by TypeSafe direct; `usage.input_tokens` per call was not printed by this replay version, so the token total is not recorded here.

## What this says about version 1 of the decision

The state and questions shipped in `loop/judgment.go` do not separate a false closure from a legitimate candidate: the false ones are near the top, but 0.76 and 0.66 are not far from the legitimate 0.60–0.63. The likely cause, from the state shape: the judge receives the executor's last 60 lines and the changed-path list, and is asked to infer the contradiction itself. TypeSafe's own guidance is to compute in code what code can compute. The next iteration should extract the executor's claimed paths and claimed proof results in code, compare them with `changed_paths` and the proof verdicts, and pass the judge an explicit `claims_without_evidence` list plus the raw report, asking a narrower `noul` ("the listed claims are material to the task"). That is a change to the state builder only; the judge wiring, journal records and replay stay.

Nothing in this file should be quoted as an improvement. It is the baseline.

## Prospective run 1 — `judge-polish` with the judge in shadow (2026-09-21)

Delivery `judge-polish-20260921-012155`, run with a `cmd/batuta` binary built from main at `c777b88` plus the plan commit, `.batuta/judge.json` = `{"provider":"auto","timeout_ms":20000,"decisions":{"claim_evidence":{"mode":"shadow","threshold":0.9}}}`, keys for the three providers in the environment. Every attempt got one `judge_intent` and one `judge_result` record; the attempt outcomes were decided by the gates alone (shadow).

Verdicts recorded live in the journal (`judge_result.detail`):

| attempt | recorded outcome | claim_unsupported | verifier_contradicted | latency | input tokens | provider / model |
|---|---|---|---|---|---|---|
| task_1 e1 | candidate | 0.24 | 0.15 | 718 ms | 2780 | typesafe / jev-1.13.0 |
| task_2 e1 | tests_failed (`TestJudgeReplayJSON` failed) | 0.85 | 0.85 | 683 ms | 3068 | typesafe / jev-1.13.0 |
| task_2 e2 | candidate | 0.40 | 0.10 | 708 ms | 2478 | typesafe / jev-1.13.0 |

The same delivery replayed afterwards with `batuta judge replay` (state rebuilt from the journal and the run logs, polished binary at `3b80782`):

```
task_1 e1 outcome=candidate claim_unsupported=0.41 verifier_contradicted=0.18 provider=typesafe tokens=2383/45 ms=674
task_2 e1 outcome=tests_failed claim_unsupported=0.86 verifier_contradicted=0.86 provider=typesafe tokens=2738/45 ms=250
task_2 e2 outcome=candidate claim_unsupported=0.38 verifier_contradicted=0.12 provider=typesafe tokens=2229/45 ms=247
attempts=3 asked=3 skipped=0 input_tokens=7350 output_tokens=135
```

Observations, numbers only:
- Live and replayed states are not identical: input tokens differ by 250–400 per attempt (the live state is built from the executor result in memory, the replay from the `.out.log` on disk), and `task_1 e1` moved from 0.24 live to 0.41 in replay. Comparisons between live and replay must account for this; comparisons within one mode are fine.
- The one real gate failure (task_2 e1, a test the executor itself wrote and claimed passing) scored 0.85/0.86 in both modes; the two legitimate candidates scored 0.24–0.41 / 0.10–0.18. On this delivery the separation is clear, unlike the retroactive baseline.
- Cost of the shadow judge for the whole delivery: 7350 input tokens over 3 calls, about $0.0003 at $0.042/M; added wall time about 0.7 s per attempt live.

## Version 2 replay — atomic claims, code first (2026-09-21)

Binary built from `844d619` (plan `judge-claims-v2`, merged locally on main), same command as the baseline, same 23 retroactive attempts plus the 7 attempts of the two deliveries run since. Raw output copied verbatim to `.batuta/judge-replay-v2-raw.txt`. Fields: `claims` extracted from the executor report, `code_contradicted` settled by code, `judge_contradicted` by the judge, `uncertain` bucket, `max_contradicted` the highest judge probability of "contradicted", `flagged` the aggregate.

Headline numbers:
- Both known false closures are flagged: core `research-ladder task_3` (claims=4, code_contradicted=1, judge max 0.03) and skills `research-ladder task_4` (claims=3, code_contradicted=1, judge max 0.01). Code caught them through the claimed path that the tree did not change. The judge contributed nothing to either.
- `judge_contradicted=0` on every one of the 30 attempts. The judge answered `unverifiable` on every criterion claim it was asked.
- 9 of 19 legitimate candidates are also flagged (`flagged=true` with `code_contradicted` between 2 and 15), which would make `enforce` unusable as replayed.

Why, from the claim dumps (`--json`) rather than from the totals:
1. The journal records changed paths only when the scope gate fails (`gates.Scope` puts them in `Detail` on failure). For passing attempts the replay rebuilt the state with an empty `changed_paths`, so every path claim was "contradicted" by code. This is a replay artifact: the live path has the real list. The retroactive path numbers above are therefore not evidence about the extractor, and the two true positives were caught for a reason the replay cannot distinguish from this artifact (their trees were unchanged, which code does see).
2. The path extractor accepted `typesafe/jev-1.13`, `encoding/json`, `github.com/batuta-ai/core/judge` and a branch name as repository paths.
3. `BATUTA-PROGRESS n DONE` and `TASK n: DONE` claims were emitted without their criterion index, so no proof verdict or verifier line was attached; the judge received a claim with empty evidence and answered `unverifiable` (confidence 0.30–0.84).

So version 2 as built does not yet test the design. Version 2.1 fixes the three defects and reruns this exact replay; until then no number here supports or refutes the judge.

## Version 2.1 replay (2026-09-21)

Binary built from `f9454c0` (plan `judge-claims-v21`, tasks 1–2 integrated by the loop; task 3 done by the conducting host because a worktree executor cannot read journals outside its worktree). Judge `provider: auto` → TypeSafe direct. Raw output verbatim in `.batuta/judge-replay-v21-raw.txt`; every count below was computed from that file.

| measure | value |
|---|---|
| finished attempts replayed | 33 (11 journals: 9 core, 2 skills) |
| legitimate candidates | 24 |
| known false closures | 2 |
| false closures flagged | 2 of 2, both by code (`changed_paths=0`, claimed path), judge not asked |
| legitimate candidates flagged | 7 of 24 |
| attempts where the judge was asked | 17 |
| claims extracted / settled contradicted by code / contradicted by judge / uncertain | 403 / 43 / 0 / 35 |
| highest judge "contradicted" probability on any claim | 0.33 |
| attempts with `changed_paths=unknown` | 6 (all in the skills journals: the replay resolves candidate commits with git in the current workspace, and those commits live in the skills repository) |
| gate-failed attempts flagged | 2 of 7 (one `tests_failed`, one `scope_violation`) |

What changed from version 2: the replay now has real changed paths for every core attempt (`scope.paths` recorded live from this version on, `git diff --name-only base..candidate` for older journals), and criterion claims reach the judge with their proof verdict and verifier line attached.

What did not change: 7 legitimate candidates are still flagged by code. The `--json` claim dump shows every one comes from a token accepted as a path claim although the report only mentions it: Go import paths (`encoding/json`, `github.com/batuta-ai/core/judge`), a model id (`typesafe/jev-1.13`), branch names (`batuta/judge-package/task-1-e1`), bare extensions (`.go`, `.md`) and files named as examples (`docs/missing.md`). The task 2 implementation let `known` accept any token with a known source extension, and the extractor still takes backticked tokens from any line rather than from "Paths touched" lists or edit-verb sentences. That is a specification defect in this plan, not a judge result.

What the judge did: asked 17 times with evidence attached, it never answered `contradicted` above 0.33 on any claim; 35 answers landed in the uncertain bucket. On this corpus there is no case where the judge found a contradiction that code had not already settled.

Decision rule stated before the run (research note, section 7): keep `enforce` only if both false closures are flagged and no legitimate candidate is flagged. Result: the first half holds through code, the second does not because of the extractor. Version 2.2 must restrict path claims to explicit edit statements before the rule can be evaluated; the judge's own contribution so far is zero contradictions.

## Version 2.2 replay (2026-09-21)

Binary built from `1fdb0ca` (plan `judge-claims-v22`: edit-statement path claims, untrusted-data note, key-pointing questions, positive-evidence criteria with concrete defect labels, `material` question). Same command over 12 journals (10 core, 2 skills), judge TypeSafe direct. Raw output verbatim in `.batuta/judge-replay-v22-raw.txt`; counts computed from that file.

| measure | v2.1 | v2.2 |
|---|---|---|
| finished attempts replayed | 33 | 36 |
| legitimate candidates flagged | 7 of 24 | **0 of 26** |
| known false closures flagged | 2 of 2 (code) | **0 of 2** |
| attempts where the judge was asked | 17 | 17 |
| claims / code contradicted / judge contradicted / uncertain | 403 / 43 / 0 / 35 | 392 / 35 / 0 / 31 |
| highest judge "contradicted" probability | 0.33 | 0.05 |
| highest `material` probability | — | 0.82 |
| gate-failed attempts flagged | 2 of 7 | 2 of 8 |

Precision is fixed: no legitimate candidate is flagged any more, and the judge's contradiction probability collapsed to at most 0.05 with the positive-evidence criteria. Recall broke: the two false closures lost their path claim. Their reports (agy) write the touched file as `### Paths touched` followed by `- [.batuta/routing.md](file:///…/.batuta/routing.md)` (skills task_4) and as `Touched path:` followed by `- [` `.batuta/routing.md` `](file:///…)` (core task_3). The v2.2 extractor accepts only the heading order `(paths|files) (touched|changed|modified|edited)` and does not read a Markdown link as a path, so `claims=1` there is not the path and nothing contradicts. Version 2.3 fixes recall in the extractor only (heading in either order, singular or plural; Markdown link text or `file://` target relative to the worktree; a few more edit verbs) and reruns this replay. The judge itself was asked 17 times and again contradicted nothing; `material` reached 0.82 on one claim.

## Version 2.3 replay (2026-09-21)

Binary built from `de604eb` (plan `judge-claims-v23`: touched-file headings in either order, Markdown-link items, more edit verbs; the v2.2 precision gate kept). Same command over 13 journals (11 core, 2 skills), judge TypeSafe direct. Raw output verbatim in `.batuta/judge-replay-v23-raw.txt`; counts computed from that file.

| measure | v2.1 | v2.2 | v2.3 |
|---|---|---|---|
| finished attempts replayed | 33 | 36 | 37 |
| known false closures flagged | 2 of 2 | 0 of 2 | **2 of 2** (code, `changed_paths=0`, judge not asked) |
| legitimate candidates flagged | 7 of 24 | 0 of 26 | **0 of 27** |
| attempts where the judge was asked | 17 | 17 | 18 |
| claims / code contradicted / judge contradicted / uncertain | 403 / 43 / 0 / 35 | 392 / 35 / 0 / 31 | 402 / 37 / 0 / 33 |
| highest judge "contradicted" probability | 0.33 | 0.05 | 0.07 |
| highest `material` probability | — | 0.82 | 0.84 |
| gate-failed attempts flagged | 2 of 7 | 2 of 8 | 2 of 8 |

Decision rule stated in the research note before the runs: keep `enforce` only if both false closures are flagged and no legitimate candidate is flagged. **Version 2.3 meets it on this corpus.** Both halves come from the code settlement (claimed path, unchanged tree); the judge was asked 18 times over prose claims with evidence attached and contradicted nothing, so its measured contribution to this decision on this corpus is zero contradictions. `enforce` therefore protects against the observed defect class through code, and the judge's calls are a cost (18 calls, about 2.5k input tokens each) without a detection yet. Whether the judge earns its place on `claim_evidence` needs the constructed corpus (prose claims with correct paths that the evidence refutes), not more replays of this one.

## Constructed corpus, run 1 (2026-09-21)

Decision rule frozen beforehand in `.batuta/judge-research.md` section 10 (commit `6620423`). Binary built from `f2cc89c` (branch `feat/judge-corpus`; final review of delivery `judge-corpus-fixes-20260921-213457` returned SHIP with no findings). Judge config `.batuta/judge.json` (excluded from git) was exactly `{"provider":"auto","timeout_ms":20000,"decisions":{"claim_evidence":{"mode":"shadow","threshold":0.9}}}`. One trial per repository, 2026-09-21 22:55:45Z to 22:56:38Z.

Commands: `batuta judge corpus build` over every core journal except `judge-corpus-*`, and over every skills journal from the skills workspace; then `batuta judge corpus run --corpus <file> --json` (skills with `--config ../core/.batuta/judge.json`). The corpus files are 9.3 MB and 0.6 MB and are not committed; the build is deterministic and their sha256 are in `.batuta/judge-corpus-v1/corpus.sha256`. Raw run output, skip lists and the offline code-contradiction dump are in `.batuta/judge-corpus-v1/`. Every count below was computed from those files.

| measure | core | skills | total |
|---|---|---|---|
| source attempts (legitimate candidates) | 97 | 15 | 112 |
| attempts skipped (not `candidate`) | 29 | 5 | 34 |
| cases | 383 | 45 | 428 |
| judge calls / unavailable | 155 / 0 | 22 / 0 | 177 / 0 |
| input tokens (from usage) | 275,961 | 36,133 | 312,094 |

| label | cases | flagged | flagged by code | flagged by the judge | missed |
|---|---|---|---|---|---|
| `fabricated_reference` | 112 | 112 | 112 | 0 | 0 |
| `wrong_count` | 92 | 92 | 92 | 0 | 0 |
| `behaviour_absent` | 112 | 17 | 17 | **0** | 95 |
| `clean` (false flags) | 112 | 7 | 7 | 0 | — |

(`skills` has no `wrong_count` cases: no source attempt changed a `_test.go` file. The runner's `flagged_by_judge` column counts cases whose first contradicted claim came from the judge, whether or not the aggregate flagged them; the table above counts aggregate flags only.)

Against the frozen rule:
- Sanity, code flags ≥90% of `fabricated_reference` and `wrong_count`: 112/112 and 92/92. **Holds.**
- The judge flags ≥50% of `behaviour_absent`: **0/112. Fails.** The judge does not earn its place on `claim_evidence` on this corpus.
- The judge flags ≤5% of `clean`: 0/112. Holds, trivially.

What the numbers show beyond the rule, without restating it at another threshold:
- The judge did answer: on 18 of 112 `behaviour_absent` cases its contradicted probability was between 0.93 and 0.99; on `clean` cases it never exceeded 0.5. None of the 18 became a flag, because the aggregate also requires the choice's confidence and the `material` answer to reach 0.9, and the per-case output does not print which of the two fell short. Even counted as flags, 18/112 is 16%, below the 50% bar.
- Every code flag on `behaviour_absent` and on `clean` is a path claim, not detection of the defect (`code-contradictions-*.txt`). Three causes: the corpus runner calls `ExtractClaims` without the `known` path gate that the live loop applies (bare basenames such as `panel_model.go` become claims); a Markdown link whose target is a relative `.batuta/worktrees/<name>/…` path is not rewritten, so the claim keeps the `[text](target` form; and the `behaviour_absent` rule borrows another task's title, which sometimes names a path (`docs/review.md`, `routing.md`) that the case did not change. The first two are runner and extractor defects to fix before any rerun; the third is a property of the frozen variant rule and stays as is for this run.
- Cost of the judge on this corpus: 177 calls, 312,094 input tokens, 53 s wall time, for zero flags.

Headline: on 112 legitimate attempts with a borrowed, false behaviour claim about a real changed file, the judge flagged none; code settled every fabricated identifier and wrong test count. `claim_evidence` stays code-first; the judge stays in shadow.

## Plan classification, run 1 (2026-09-22)

Decision rule frozen beforehand in `.batuta/judge-research.md` section 11 (commit `eba9c19`). Binary built from `7ba6ead` (branch `feat/judge-classify`; final review of delivery `judge-classify-fixes-20260922-112834` returned SHIP with no findings). Judge config `.batuta/judge-classify-v1/judge-classify.json`: `provider: auto`, `classify` threshold 0.7. One trial, 2026-09-22 12:04:47Z to 12:05:49Z. Command: `batuta judge classify bench` with one `--plan` per finished plan, `--journals` for the core and skills journals, `--json`. Raw output in `.batuta/judge-classify-v1/run.jsonl`; every count below was computed from it.

Deviation from the frozen inputs, stated before reading the result: the first launch stopped before any judge call because `core/.batuta/plans/done/supervision-fixes.md` (2 tasks) has a free-text `Status` line the plan parser rejects (`.batuta/judge-classify-v1/parse.txt`). That plan was excluded; the run covers the other 51 plans and 181 tasks (`plans-run.txt`). Nothing else changed between the two launches.

| measure | value |
|---|---|
| tasks | 181 (labels: 84 high, 84 medium, 13 low, 0 critical) |
| judge answers | 171 high, 10 critical, 0 medium, 0 low |
| exact complexity agreement | 80 / 181 = 44.2% |
| constant-answer baseline | 84 / 181 = 46.4% (most common label; high and medium tie) |
| under-routed (judge lower than label) | 0 |
| over-routed (judge higher than label) | 101 / 181 = 55.8% |
| domain agreement | 131 / 181 = 72.4% |
| fallbacks (a confidence below 0.7) | 55: 10 on complexity (to critical), 46 on domain (to general) |
| complexity confidence | median 0.93, min 0.32, max 0.99 |
| judge calls / unavailable | 181 / 0 |
| input tokens (from usage) | 281,129 |

Confusion matrix, label (rows) against judge (columns):

| label \ judge | low | medium | high | critical |
|---|---|---|---|---|
| low (13) | 0 | 0 | 9 | 4 |
| medium (84) | 0 | 0 | 82 | 2 |
| high (84) | 0 | 0 | 80 | 4 |

Against the frozen rule:
1. Exact agreement ≥80%: 44.2%. **Fails.**
2. At least 20 points above the constant-answer baseline: 44.2% against 46.4%, which is 2.2 points *below* it. **Fails.**
3. Under-routed ≤10%: 0%. Holds, because the judge never chose a lane below `high`.

The judge is not fit to classify plan tasks on this corpus. It answered `high` for 171 of 181 tasks with a median confidence of 0.93, including 82 of 84 tasks the host labelled `medium` and 9 of 13 labelled `low`. Confident and uniform, not uncertain.

Measure (b), reported only, for tasks whose delivery journal was found (131 of 181):

| judge vs executed lane | first attempt candidate | retried | failed | escalated | no journal |
|---|---|---|---|---|---|
| equal | 26 | 11 | 9 | 0 | 34 |
| higher | 59 | 18 | 8 | 0 | 16 |
| lower | 0 | 0 | 0 | 0 | 0 |

Of the 85 over-routed tasks with a journal, 59 succeeded on the first attempt at the host's lower lane. Following the judge would have sent them to a more expensive executor with no need shown by the outcome.

Headline: asked to pick a lane from the task text with the skills' own rubric, Jev answered `high` for 94% of tasks and agreed with the host less often than a constant answer would.

## Reanalysis of constructed-corpus run 1 — post hoc, not a result (2026-09-22)

Asked by the maintainer whether Jev was applied correctly, the conductor recomputed from `.batuta/judge-corpus-v1/run-*.jsonl` the judge's own contradicted probability (`max_contradicted`) instead of the aggregate flag. Over the cases where the judge was asked (103 `behaviour_absent`, 27 `clean`):

| threshold on `max_contradicted` | `behaviour_absent` at or above | `clean` at or above |
|---|---|---|
| 0.5 | 92 / 103 | 0 / 27 |
| 0.7 | 76 / 103 | 0 / 27 |
| 0.9 | 33 / 103 | 0 / 27 |

Area under the ROC curve: 0.968. The judge separated the borrowed behaviour claims from clean reports well; the 0/112 of run 1 came from the aggregation the batuta built around it (a defect choice counted only with choice confidence ≥0.9 *and* the `material` answer ≥0.9) and from an `unverifiable` criterion that told the model a short slice is not contradiction. These thresholds were chosen after seeing the data, so none of this is a result under the section 10 rule; it motivates plan `claim-evidence-v3`, which calibrates on one half of a new corpus and tests on the other with the split and rule frozen beforehand. Caveats: only 27 clean cases were asked at all (a clean report rarely carries a behaviour claim), and a borrowed task title may be an easy defect.

The same question for plan classification: the `high` criterion ("subsystem or multi-file work fully captured by a precise brief") describes almost every batuta plan task by construction (a closed Scope with source and test files, a precise brief), and the judge applied it literally. Run 1 measured a rubric that cannot separate `medium` from `high`, not the judge's ability; only the chosen option was kept, so no probability reanalysis is possible there.
