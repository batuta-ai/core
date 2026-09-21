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
