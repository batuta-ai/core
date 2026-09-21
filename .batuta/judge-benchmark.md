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
