# How batuta should apply Jev — a study (2026-09-26)

Written on branch `docs/jev-application-study` after core#136 merged. Sources: `.batuta/judge-research.md` (R), `.batuta/judge-benchmark.md` (B), the three astra reviews (`judge-review-astra*.md`), the code under `judge/`, `classify/`, `loop/judgment.go`, the raw run `.batuta/judge-classify-v2/run.jsonl`, and the maker's documentation at docs.typesafe.ai. Every number below comes from one of those; the post-hoc numbers in section 4 are new, computed from the raw run, and decide nothing.

## 1. What Jev is, and what that means for batuta

Jev (`jev-1.13.0`, TypeSafe "System One") is not an LLM. It takes a `state` plus typed questions and returns typed answers with probabilities: `noul` (yes/no probability, no confidence), `choice` (option, full distribution, confidence), `score` (weighted level, confidence). It cannot generate text, call tools, or explain itself. Limits: 64k tokens per request, 32k for the state; $0.042 per million input tokens, output free; 92–214 ms per call independently, 250–720 ms in batuta's own runs (R:7-10, B:59-61).

The maker's own jaggedness page says what it is bad at: literal reading of instructions, arithmetic and counting, dates, multi-hop reasoning and double negatives, irrelevant context, injected instructions, and a forced `choice` with no escape option (answered wrong at confidence 0.31 in an independent test) (R:11). Its guidance: do math and dates in code, filter the state, use `choice` for bounded spaces, one explicit criterion per option, start with conservative thresholds and calibrate on your own data. Third-party numbers agree: on a closed rubric Jev matches an LLM judge (94.3% vs 95.5%) with a better Brier score (0.043 vs 0.054); on an open rubric over long context it is clearly worse (Spearman 0.47 vs 0.72).

So the model is a **cheap, fast, reasonably calibrated classifier over a small packet whose decisive fact is present**. It is not a reader of raw state, not a reasoner over what is missing, and not a substitute for code that can compute the answer.

One infrastructure delta found during this study: OpenCode Zen documents a native endpoint `https://opencode.ai/zen/v1/systemone` with `jev-1.13` and a free `jev-1.13-free`, same request shape as TypeSafe. The 2026-09-20 probe that declared `opencode/jev-1.13-free` non-viable went through opencode's chat path (R:21). `judge/http.go` already parametrises `base_url`, `model` and `key_env`, so a re-probe costs no code. Untested.

## 2. What twelve runs taught

Five experiments, twelve recorded runs, all in shadow, none positive under its frozen rule. The pattern is the same each time: the question asked Jev to infer something the packet did not contain, or the rule turned its uncertainty into an action.

| Run | What Jev was asked | Result | Why it failed (as recorded) |
|---|---|---|---|
| claim_evidence v1 replay (B:1-49) | Two global `noul` over the whole attempt: "report claims work the evidence does not show" | false closures at 0.76 and 0.66, legitimate 0.30–0.83; 0 TP at 0.9 | inference from raw state; scores compressed to 0.3–0.8 (same as BargLabs, ECE 0.42–0.458, R:158-164) |
| claim_evidence v2–v2.3 (B:77-150) | Per atomic claim: "is there positive evidence this claim is false", evidence slice ≤1,800 bytes | code flagged 2/2 false closures every time; judge contradicted 0 claims in 18 calls at v2.3, max probability 0.07 | judge answered `unverifiable` whenever the slice said nothing decisive; the slice usually said nothing decisive |
| constructed corpus run 1 (B:152-185) | same per-claim question over 428 synthetic cases | `behaviour_absent` 0/112 by judge; 18 cases scored 0.93–0.99 but a second gate (`material` ≥0.9) blocked all | double gate; synthetic labels; retrieval never measured separately |
| claim_evidence v3 calibration (B:248-271) | same, no double gate, calibrate/test split | no threshold 0.50–0.95 keeps false flags ≤2%; at 0.50 flags 36/40 `behaviour_absent` and 21/40 `true_behaviour` | 1,800-byte slice makes true and false claims look alike; test half never run |
| classification v1 (B:187-232) | `choice` complexity low/medium/high/critical + domain from task text | 171/181 "high", confidence median 0.93; 44.2% vs 46.4% constant baseline | rubric's `high` described every task; only the chosen option was kept, no reanalysis possible |
| classification v2 (B:277-297) | five `noul` on task text; code measures Scope; uncertainty (<0.7) counts as "yes" | 197/228 critical; economy 11.9%; `open_decision` never yes at ≥0.7, median 0.62 | open question ("depends on a decision the plan does not state") needs reasoning over absence; the default rule turned every uncertain answer into the top lane |

Three lessons hold across all of them:

1. **Absence questions fail.** "Is this false", "is something missing", "does it depend on an unstated decision": each asks Jev to reason about what the packet does not say. Its `unverifiable` and its 0.62 confidence are the honest answer. Code, or a human, owns absence.
2. **Rules that act on uncertainty fail.** The 0.9 double gate hid 18 true positives; the "<0.7 means yes" default put 86% of tasks in `critical`. Uncertainty must land in a distinct `insufficient` outcome that code treats as "no information", never as "yes" or "no".
3. **Retrieval, model and policy were measured as one thing.** Every negative headline blames the slice, the rubric or the gate after the fact. None of the runs recorded whether the decisive passage was in the packet.

The astra review said the same in 2026-09-22 (judge-review-astra.md:5, :17, :126; -workflow.md:117-147): closed options with `insufficient`, relation separated from action, freeze the whole policy including the pinned model, measure the three layers separately. Of its recommendations, the doctrine items were adopted (code first, Jev never over permissions/SHIP/publication, residue = after deterministic checks, `verifier-coverage` shipped) and the methodology items are still open (mutually exclusive options, retrieval sufficiency, group-wise reporting, negative sizing, staged rollout).

## 3. What the classify v2 raw run says about Jev on task text (post hoc, decides nothing)

Computed from `.batuta/judge-classify-v2/run.jsonl`, 228 tasks, 171 with a known outcome; the frozen label is "insufficient" = `failed` or `escalated` at the first lane (20 of 171).

Rate of insufficient outcomes when a signal is present vs absent:

| Signal | present | absent |
|---|---|---|
| Jev `contract` yes | 14/117 (0.12) | 6/54 (0.11) |
| Jev `lifecycle` yes | 12/97 (0.12) | 8/74 (0.11) |
| Jev `security` yes | 8/76 (0.11) | 12/95 (0.13) |
| Jev `open_decision` yes | 17/140 (0.12) | 3/31 (0.10) |
| Jev `mechanical` yes | 1/7 | 19/164 (0.12) |
| Scope `Files ≥ 4` (code) | 16/96 (0.17) | 4/75 (0.05) |
| Scope `Files ≥ 7` (code) | 10/40 (0.25) | 10/131 (0.08) |
| Scope `DocsOnly` (code) | 4/16 (0.25) | 16/155 (0.10) |
| host lane `low` | 3/15 (0.20) | 17/156 (0.11) |

None of the five Jev answers moves the insufficient rate. Scope size does, and `DocsOnly` tasks fail more often than their `low` routing assumes. The whole run cost 278,619 input tokens, about $0.012 at the list price.

Two readings. First, for lane classification the signal that exists lives in the plan's Scope, which code already measures; a rule built on Scope alone is the baseline any v3 must beat. Second, the questions were not wrong in themselves; they were asked about a task excerpt that does not contain what decides them. "Does it change a public contract" is decided by the diff, not by the brief. That is the general rule for where Jev pays: the decisive fact must be in the packet.

## 4. Principles — how to extract what Jev is good at

1. **Code selects the packet, Jev names the relation, code acts.** Jev never sees raw state. The packet is one bounded thing (one verifier line, one question, one finding, one terminal message) plus the passage code found for it. If code cannot find a passage, the answer is `insufficient` without a call.
2. **Closed `choice` with an escape option, always.** Options mutually exclusive, one explicit criterion each, plus `insufficient` (or `unclear`). Never a bare `noul` where "no information" is a possible truth; the v2 noul had no way to abstain, and the rule invented one.
3. **Relation, not action.** Ask "which of these does the evidence show" and let code map the answer to an action. "Environment-only" is a relation; "safe to accept" is an action (judge-review-astra-workflow.md:109).
4. **Nothing that reasons over absence, arithmetic, dates or multiple hops.** Those go to code. The maker says so; every batuta run confirmed it.
5. **Confidence gates an action, never flips an answer.** Below the band the outcome is `insufficient` and the code path that ran before Jev existed keeps running. Uncertainty may never raise or lower a lane, clear or add an objection.
6. **Keep the whole distribution.** Record `probabilities` and `confidence` for every answer, not only the chosen option, so a reliability table can be built and a rule reanalysed without a rerun (v1 classification lost this, B:246).
7. **Measure three layers separately, before the model is blamed.** Retrieval: did the packet contain the decisive passage (adjudicated on a sample). Model: given a sufficient packet, was the relation right. Policy: given the relation, was the action right and what did a wrong action cost.
8. **Freeze the policy, pin the model, split by delivery.** Rule, options, packet builder, threshold, action and `model: jev-1.13.0` written before the run; calibrate/test halves split by delivery (not by task) so a plan's siblings do not leak; negatives sized to the error budget (59 for <5%, 149 for <2%, 299 for <1%, -workflow.md:131-137); groups reported separately so easy negatives do not dilute a rate.
9. **Credit only against the code-only baseline.** Jev's price is not the cost; a false alarm costs a retry (one correction cycle: 8 min, 80k tokens, learnings.md:18) and a missed alarm costs whatever the loop already pays. A decision ships only when the measured net is positive over the baseline the loop already has.
10. **Stage it.** Offline replay → shadow (journaled, no effect) → visible annotation in the trail → narrow enforce with an explicit revert. Jev has no authority over permissions, SHIP, publication or skipping a verifier at any stage.

## 5. Where Jev pays in batuta, ranked

Ranked by (decisive passage fits in one packet) × (measured cost of the decision today) × (an existing code seam to hang it on).

| # | Decision | Packet | `choice` options | Code action on answer | Cost today | Seam |
|---|---|---|---|---|---|---|
| 1 | **Verifier-objection triage** | one `INCOMPLETE` line + its criterion + the proof result | `environment_only` / `substantive_gap` / `mixed` / `insufficient` | annotate the trail; never clears the objection; a `substantive_gap` on a passed proof surfaces for the conductor | 14 verifier runs, 814 s, ~58 s each; 87 verifier results in 169 gate reports | regex at `gates/gates.go:411, :440` already sets objections aside |
| 2 | **Question-to-plan matching** | one `BATUTA-QUESTION` + the approved plan passage code retrieves for it | `answered_here` / `unresolved_decision` / `scope_or_permission_change` / `insufficient` | `answered_here` → hand the passage to the executor as a routine answer; the others park as today | 21/46 first executions ended in a question (lanes:79) | `recordQuestion` at `loop/attempt.go:786-833`; supervision already answers on exact digest match (`loop/supervision_policy.go:187-194`) |
| 3 | **Finding-to-contract consistency** | one review finding + one plan rule (Decisions/Scope line) | `conflicts` / `consistent` / `unrelated` / `insufficient` | `conflicts` → tag the finding "would weaken the contract" for the conductor; never suppresses | one correction cycle 8 min / 80k tokens | `review.Merge` at `review/findings.go:178` |
| 4 | **Scout/brief evidence check** | one brief claim + the scout passage it cites | `supports` / `explicitly_conflicts` / `insufficient` | flag before the worker starts | not measured | none yet |
| 5 | **Provider-limit classification** | one terminal message | `actual_limit` / `quoted_or_example` / `other_failure` / `insufficient` | choose wait vs retry | 0 real limits in 202 logs; a false limit costs a 30-min wait | parked until telemetry collects positives |
| 6 | **Adversarial-review trigger** | one criterion + its proof command | `exercises_concurrent_path` / `does_not` / `insufficient` | make the adversarial pass mandatory | none | Tempo triage item, later |
| — | Lane classification from task text | task brief | — | — | v1 and v2 negative; Scope carries the signal | keep code-only unless a diff-based question is found (section 6) |
| — | Broad claim checking | attempt state | — | — | v1–v3 negative; code settles what matters | shadow only, no routine calls |

Items 1–3 share one shape: one line, one passage, one relation, an annotation. They can share one packet builder and one calibration harness (`judge corpus` generalised beyond claims), which is the real cost of starting.

## 6. Classification v3 — what it should be, given the decision to do it first

The maintainer chose v3 before verifier triage. The data in section 3 says a v3 that asks Jev the same kind of question about the same text will fail the same way. The version worth freezing:

**Step 0, code-only baseline (no Jev).** Lane from Scope alone: `Files`, `Directories`, `DocsOnly`, `TestOnly`, plus open-decision detection by code from the plan text (an explicit question mark in the task, a "choose/decide/TBD/either" marker, or an option list without a chosen option). Measured under the existing v2 rule (economy ≥75%, safety ≥50%, largest lane ≤70%, balance ≥0.65) on the same 228 tasks, split by delivery into calibrate/test with the mapping frozen after the calibrate half. This is the number Jev must beat; without it a positive v3 proves nothing about Jev.

**Step 1, Jev only on questions whose decisive fact is in the packet.** Candidates, each a `choice` with `insufficient`:
- `contract`: packet = the Scope file list + the exported identifiers code finds in those files (from `go doc`-style extraction or a grep of `^func [A-Z]`, `^type [A-Z]`, flag registrations). Question: "which of these does the task text ask to change: an exported identifier or flag listed / only unexported or internal code / insufficient". The identifiers are in the packet; Jev matches text to list.
- `security`: packet = task text + the list of Scope paths under `permissions/`, `sandbox`, `redact`, `secret`, `auth`. Same shape.
- Drop `open_decision` (absence) and `lifecycle` (needs the diff). Drop `mechanical` or fold it into the code baseline (`DocsOnly` + ≤2 files).

**Step 2, weighted mapping, firm answers only.** An `insufficient` or low-confidence answer contributes zero, never a lane change. The mapping is frozen on the calibrate half; the test half reports.

**Honest caveats to write into the rule.** The corpus has 0 tasks that ran at `critical` and 15 at `low`, so economy and safety are only measurable where lanes actually varied; `retried` is counted as sufficient although some retries are environment failures; 57/228 tasks have no journal. A v3 result, positive or negative, is a statement about routing between `medium` and `high` on batuta's own plans, nothing wider.

**Expected outcome, said before the run.** Step 0 probably clears economy and discrimination and fails safety (Scope size predicts failure at 0.25 vs 0.08 but 20 positives is a thin label). Step 1 probably adds little on top. If that is the result, classification stays code-only and Jev's budget goes to section 5 items 1–3, which is where the packet shape fits the model.

## 7. What to build once, for every decision

- A packet builder interface: `(seam, id) → packet{lines, passage, digest}` with a `found` flag; no `found` → `insufficient`, no call.
- A `choice`-with-escape helper in `judge/` that refuses a question without an `insufficient` option and always keeps `probabilities` + `confidence` in the `judge_result` record.
- `judge calibrate` generalised: takes any labelled packet set, reports per-group rates, a per-bin reliability table (probability bin → observed rate), and the lowest threshold meeting a stated false-rate budget; refuses to report a test half before a calibrate half is frozen.
- `model` pinned in `.batuta/judge.json` for benchmark runs (`jev-1.13.0`, never `jev-latest`).
- A cost line per decision in the trail: calls, input tokens, unavailable count, and the action taken; money stays out until billing data exists.
- Re-probe the OpenCode Zen `systemone` route as a fourth provider in `auto`.

## 8. What Jev must never do in batuta

Decide permissions, SHIP, publication or a merge. Clear a verifier objection or skip a required verifier. Change a lane on an uncertain answer. Answer an executor's question itself (it only says whether the plan already answers it). Run on every poll. Be credited with a saving without the code-only baseline beside it.

## 9. Open questions for the maintainer

- v3 with step 0 as a mandatory baseline, or go straight to verifier-objection triage (section 5, item 1) and revisit classification when a diff-based packet exists?
- Adjudicate the 20 insufficient v2 tasks by hand: how many failed for lane reasons vs environment? That decides whether "outcome at first lane" is a usable label at all.
- Re-probe OpenCode Zen before the next bench, so the free route is available if TypeSafe rate-limits.
