# Judge research — Jev as a decision model inside the batuta core

Date: 2026-09-20. Status: research, no implementation authorized. Requested by the user after two false "already satisfied" closures on the same day (fixed for proofs in PR #96; the judgment gap remains).

## 1. What Jev is

- TypeSafe AI's "System One" model. Not an LLM: it takes a `state` (string or JSON) plus a map of typed questions and returns typed answers with probabilities. It does not generate text, cannot read a repository, cannot run commands.
- Question types: `noul` (yes/no → probability), `choice` (one of ≤255 options → chosen option, full distribution, `confidence`), `score` (ordered levels → probability-weighted value, `confidence`). All questions in one call are evaluated in parallel against the same state.
- Limits: 64k tokens per request, 32k for `state` plus the longest question; text only; English first. Rate limits 250k tokens/s, 1200 req/min, adjusting without notice. Latency measured independently at 92–214 ms per call.
- Price: $0.042 per million input tokens, output free. A judgment over a 10k-token state costs about $0.0004.
- Documented weaknesses ("jaggedness"): literal reading of instructions, arithmetic and counting, date ordering and intervals, multi-hop reasoning and double negatives, large irrelevant context, adversarial content and prompt injection. A `choice` without an escape option picks something anyway with low confidence; the independent test saw `confidence 0.31` on an irrelevant question forced into three departments.
- Confidence guidance from TypeSafe: above ~0.9 act automatically; ~0.5–0.9 proceed with caution or confirm; below ~0.5 do not act, fall back to another system. Thresholds must differ by the cost of a wrong action.

## 2. Transports

| Route | Model id | Endpoint | Key | Notes |
|---|---|---|---|---|
| TypeSafe direct | `jev-latest` → `jev-1.13.0` | `POST https://api.typesafe.ai/v1/systemone`, `Authorization: Bearer` | `TYPESAFE_API_KEY` | Native shape. Response `model` reports the versioned id. Python/JS SDKs; HTTP is enough for Go. |
| OpenRouter | `typesafe/jev-1.13` | `POST https://openrouter.ai/api/alpha/decisions` (not `/v1`, not chat completions) | `OPENROUTER_API_KEY` | Same body shape. 32k context. No free tier, prepaid credits. Errors wrapped as `{"error":{...}}`. |
| Vercel AI Gateway | `typesafe-ai/jev` | Two raw HTTP routes, verified 2026-09-20 from Vercel's docs: TypeSafe-compatible `POST https://ai-gateway.vercel.sh/typesafe/v1/systemone` (TypeSafe's exact body, TypeSafe's response shape plus a `provider_metadata.gateway` object, errors as `{"message":…,"error_type":…}`) and gateway-native `POST https://ai-gateway.vercel.sh/v1/evaluate` (`boolean` instead of `noul`, camelCase `usage`) | `AI_GATEWAY_API_KEY` | The judge uses the TypeSafe-compatible route because it needs no second parser. Free until 2026-09-25 with very tight throttling (429 after a few questions); price after that not published. |
| opencode | `opencode/jev-1.13-free` | chat wrapper | — | **Does not work.** Probe on 2026-09-20: `Error: Upstream request failed: Endpoint is unavailable.` opencode speaks chat completions; Jev only answers the decisions API. Not a viable route. |

Recommendation: implement the native TypeSafe shape once in Go (`net/http`, standard library) and make the base URL and model id configurable; OpenRouter is the same body with another URL and model id. Vercel serves the same TypeSafe shape at base URL `https://ai-gateway.vercel.sh/typesafe`, so the one parser covers it too; the gateway-native `/v1/evaluate` route stays unused.

## 3. Where the core decides today

Inventory taken on 2026-09-20 over `gates`, `loop`, `review`, `routing`, `executor`, `cmd/batuta` (file:line as of main `733495f`). Decisions fall in three classes.

**Pure code, keep as is.** Gates 0–2 and scope (`gates/gates.go:140-321`), proof execution (`:345`), tree silence (`loop/attempt.go:433`), blocker priority order (`:924`), retry/escalate/abort policy (`routing/graph.go:805`), wave admission (`:332`), settlement (`loop/settle.go:91-141`), roadmap ticking, review coverage rule "not covered ⇒ REWORK" (`review/report.go:28`), verdict reduction from findings (`review/verdict.go:18`), supervision gate "only SHIP or an operator judgment clears" (`loop/supervision_gate.go:216`). Jev must never loosen any of these.

**Regex or marker parsing of model text.** These are the brittle seams:
- `gates/gates.go:403-409` `Verifier`: `TASK n: DONE|INCOMPLETE` lines; `:404` an "environment objection" regex (`sandbox|could not run|permission|no network|…`) that sets aside an INCOMPLETE when the proof passed.
- `executor/run.go:169-221` `Outcome`/`ResetTime`: usage limit detected by an adapter `limit_regex` over the last 20 output lines; reset time parsed from prose.
- `executor/run.go:187` `Question`: last stdout line starting with `BATUTA-QUESTION:`.
- `review/findings.go:49-176` and `review/spec.go:268`: marker blocks, JSON per line, severity/kind enums, order and count checks.
- `loop/supervision_review.go:418` `classifySupervisionReview`: `review.md` shape and exit-code cross-check.

**Model verdicts consumed as facts.** The read-only verifier (`loop/attempt.go:644-705`, prompt `gates/gates.go:369`), the review cohorts and spec sweep (`review/session.go:237`, `review/spec.go:170`), and the external "fit" recommendation in `routing/select.go:195` (already corrected by deterministic rejection, the right pattern).

No decision in core today has a fallback model: an unavailable verifier fails the gate (`loop/attempt.go:664-705`), an unavailable review engine leaves the gate blocked (`loop/supervision_review.go:48`, "No fallback is installed."). Supervision answers are policy-only by design (`loop/supervision_policy.go:70`, "never worker claims").

## 4. Where Jev fits, ranked

Fit test, from TypeSafe's own guidance: bounded answer space, semantic (not arithmetic) judgment, another piece of software consumes the result, state fits in 32k after code has pre-digested it.

1. **Claim-versus-evidence check on every attempt** (the incident of the day). State = plan criteria, executor's final report and `BATUTA-PROGRESS` lines, `git status`/changed paths, proof verdicts, verifier `TASK` lines. Questions: `noul` "the executor claims work that the tree evidence does not show"; `noul` "the verifier's DONE lines are contradicted by a failed proof or by the executor's own report". Action: only ever *stricter*: a high-probability contradiction turns a passing attempt into `verifier_incomplete` with the contradiction as retry feedback. Independent test showed this exact pattern at 0.93 for an unsupported success claim. Hook: `loop/attempt.go` after `report.Decide()`, before `recordCandidate`/`already_satisfied`.
2. **Classify an INCOMPLETE or a failed test as environment vs genuine** (`gates/gates.go:404` regex, `loop/attempt.go:924` blocker choice). `choice` {environment_limit, genuine_gap, unclear} over the verifier's INCOMPLETE text or the test tail. Replaces a regex that already exists because the model text is free-form; `unclear` is the mandatory escape option; `unclear` keeps today's behavior.
3. **Usage-limit detection** (`executor/run.go:169`). `noul` "this output says the provider's usage limit or quota was hit" as a second signal next to `limit_regex`, per adapter. Keep the reset-time arithmetic in code: Jev is bad at dates. Value: fewer false "failures" that are really limits, across four CLIs with different wording.
4. **Question triage** (`BATUTA-QUESTION`, `loop/attempt.go:343`, `supervision_policy.go:70`). `choice` {needs_human_decision, environment_problem, already_answered_in_plan, scope_conflict, not_a_question}. Never auto-answers; drives notification urgency and the ask file's header, and lets a policy that already exists (`continue_approved_task`) be offered only when the class is `already_answered_in_plan` with confidence above the configured bar.
5. **Review findings second opinion** (`review/findings.go`, `review/verdict.go`). Per finding: `choice` {defect, advisory, noise, contradicts_learning} with `.batuta/learnings.md` excerpts in the state ("citation verification" cookbook). Only demotion of confidence in a `SHIP`, never promotion: a `SHIP` whose findings include a high-probability `defect` becomes `FIX_BEFORE_SHIP` for the operator; a `REWORK` never becomes `SHIP`.
6. **Plan classification** (today authored by the skill, validated by `routing/classification.go:116`). `choice` for domain, `score` for complexity, `noul` for the brief test "this task needs the conversation's context" (high vs critical). Consumed as a *proposal* through the existing `ValidateClassification`, which already rejects what the artifact contradicts. Also usable by `batuta-plan`/`batuta-route` skills through a `batuta judge classify` subcommand so every host benefits.
7. **Scout report acceptance** (skills only; core reserves `.batuta/scout/` but has no scout). `noul` "the cited files and symbols support the answer" once anchors were verified by code. Lower priority: no core code path yet.
8. **Supervision notification urgency** (`loop/supervision.go:361`). `score` low/medium/high over the event and the delivery summary, feeding `--notify`. Cheap, low risk.

Not for Jev: proofs, scope, tree equality, test exit codes, counting cohorts, date/reset arithmetic, anything the routing table decides, approving a gate.

## 5. Design sketch

**Package `judge`** (standard library only, cross-platform, headless):
- `type Judge interface { Ask(ctx, state any, questions map[string]Question) (Answers, error) }` with `Question{Type: noul|choice|score, Instructions, Criteria}` and `Answers` carrying probability, `choice`, `confidence`, model version, token usage.
- `HTTPJudge` for the native shape; `Provider` selects base URL, model id, key env var and the `noul`/`boolean` type name.
- `Unavailable` is a typed error: missing key, `provider: off`, timeout, 429, 5xx, malformed answer, confidence below the decision's threshold. Every caller treats `Unavailable` as "keep today's rule".
- Every call journaled as `judge_intent`/`judge_result` with the decision name, state digest, model version, answers and confidence. No state body in the journal (it may hold executor output); the digest allows replay from the run logs.

**Config surface** (the user's requirement: env/config file):
- `.batuta/judge.json`, read like `--policy` (`DisallowUnknownFields`, size cap): `{"provider":"typesafe|openrouter|vercel|off","model":"jev-latest","base_url":"…","key_env":"TYPESAFE_API_KEY","timeout_ms":3000,"max_state_bytes":100000,"decisions":{"claim_evidence":{"mode":"shadow|enforce|off","threshold":0.9},…}}`.
- Env overrides: `BATUTA_JUDGE` (path or `off`), the key only ever from the named env var, never from a file in the repo.
- Absent file ⇒ judge off ⇒ zero behavior change. This is the fallback the user asked for.

**Safety rules** to write into the doctrine before any code:
- The judge may block, escalate, demote or annotate. It may never approve, clear a gate, answer a question or widen a scope.
- State is pre-digested by code, bounded, and never includes secrets; executor output is untrusted input to the judge (injection is a documented weakness), which is another reason verdicts only move in the strict direction.
- Every `choice` has an escape option (`unclear`/`other`); acting requires confidence above a per-decision threshold; `noul` has no confidence, so gate on the probability itself with margins on both sides (act above 0.9, ignore below 0.1, log in between).
- Shadow mode first: record verdicts for at least one full delivery per project, compare against the deterministic outcome and the operator's judgments, then enable per decision.

**Sequencing** (each a plan of its own, in this order): (1) `judge` package + config + journaling + `batuta judge ask` CLI for manual probes; (2) decision 1 in shadow, replay against the 2026-09-20 journals (`research-ladder` in core and skills) to confirm it flags the two false closures; (3) decisions 2–4 in shadow; (4) enforce per decision after review of the shadow data; (5) skills doctrine: `judge.md` reference, `batuta judge classify` in `batuta-plan`/`batuta-route`.

## 6. What Jev changes for batuta as a whole

- Cheap, fast, calibrated *judgment* becomes available to the loop at every decision point for a fraction of a cent, without a new CLI executor and without the routing table: today every model call is a full agent session.
- The conductor's brittle regex seams over model prose (verifier lines, limit detection, environment objections) get a semantic second signal with a probability, and the operator gets a number to tune instead of a pattern to maintain.
- Skills that today classify by reading doctrine (`batuta-plan` lane and domain, scout acceptance) can ask the same judge through the core binary, so every host classifies the same way.
- The "already satisfied" class of failure gets a dedicated check that reads the executor's claims against the tree, which no gate does today.
- It does not replace the read-only verifier or the review engine: Jev cannot read the repository. It judges evidence that code and those sessions produce.

## Sources

- TypeSafe docs: introduction, quickstart, `api.md`, `models.md`, `concepts/state.md`, `confidence`, `model-jaggedness/jev-1.13`, `agent-skill.md` (https://docs.typesafe.ai/llms.txt index).
- OpenRouter: https://openrouter.ai/typesafe and the decisions endpoint notes at https://jevaiguide.com/channels/openrouter/.
- Vercel: https://vercel.com/changelog/typesafe-ai-jev-now-available-on-ai-gateway, https://vercel.com/ai-gateway/models/jev, https://jevaiguide.com/channels/vercel-ai-gateway/.
- Cloudflare model page for the request/response schema: https://developers.cloudflare.com/ai/models/typesafe/jev/.
- Independent test: https://www.mindstudio.ai/blog/jev-system-one-model-classification.
- Local probe of `opencode/jev-1.13-free` on 2026-09-20 (`~/.local/share/opencode/log/opencode.log`).

## 7. Are we applying Jev the way TypeSafe says to? (study, 2026-09-21)

Read against `concepts/how-to-build-with-system-one`, `patterns` (speculative fan-out, confidence-gated routing, composite scoring), `cookbooks/citation_check`, `cookbooks/consistency_noul_cookbook` and `model-jaggedness/jev-1.13`, after the baseline in `.batuta/judge-benchmark.md`.

What version 1 of `claim_evidence` does: one state holding the whole attempt (criteria, proofs, 60 lines of executor output, changed paths, verifier verdict) and two global `noul` questions asking the model to find any contradiction.

Where that departs from the documented way:
1. **Atomic decisions, not a global verdict.** The citation cookbook checks one claim at a time with state `{claim, section}` and a `choice` `{supports, contradicts, unsupported}`; code then decides the verdict. We ask "is anything contradicted anywhere" over everything at once. The jaggedness page lists large irrelevant context and multi-hop reasoning as the top accuracy killers; our state is both.
2. **Code computes what code can compute.** "Executor says it edited `.batuta/routing.md`; `changed_paths` is empty" is a string comparison. We handed it to the model. TypeSafe's design step is explicit: keep deterministic work in code, send only relevant structured context.
3. **Use structure in the questions.** A request carries one state and many questions; each question's `instructions` can be an object holding the question plus the data it refers to. That is the intended way to ask one question per claim in a single call (speculative fan-out), not one prose paragraph.
4. **`choice` gives confidence, `noul` does not.** The confidence-gated routing pattern needs the `confidence` field; our `noul` questions only give a probability, so we gate on a raw 0.9 with nothing to say how spread the answer is. The self-consistency cookbook maps 0.30–0.70 to an explicit `uncertain` outcome instead of a single threshold.
5. **Escape option.** A `choice` without "unverifiable/other" forces an answer; the independent test saw a 0.31-confidence pick on an irrelevant question. Our v1 has no such option.

Version 2 design, following the documents:
- Code extracts atomic claims from the executor report: paths it says it touched (`Paths touched`, backtick paths in the report), criteria it marked done (`BATUTA-PROGRESS n DONE`, `TASK n: DONE`, "criterion n passed"), test claims ("suite passed", "go test ... ok"), commit claims.
- Code attaches to each claim only its evidence: the path's presence in `changed_paths`; the proof verdict and verifier line for that criterion; the tests gate verdict.
- Code settles the claims it can settle exactly (path claimed but unchanged ⇒ contradicted by code, no model call). Only claims code cannot match exactly go to the judge.
- One request, state = short task summary, one `choice` per remaining claim with `instructions` = `{question, claim, evidence}` and criteria `{supported, contradicted, unverifiable}`.
- Code aggregates: an attempt is flagged when any claim is `contradicted` with `confidence` at or above the decision threshold; `unverifiable` and 0.30–0.70 land in an `uncertain` bucket that is recorded, never acted on.
- Evaluation: the same 23 retroactive attempts plus every new delivery, replayed with the same command; the cut for keeping `enforce` is stated before running: both false closures flagged, no legitimate candidate flagged.

The honest expectation: on the two known false closures the code step alone will flag them (claimed path, unchanged tree). Jev's measured contribution will be whatever it adds on prose claims that code cannot match. That is the number the article should report.

## 8. What compozy/yoshi teaches about applying Jev (read 2026-09-21)

`github.com/compozy/yoshi` is a local context-pruning proxy for Claude Code and Codex: Jev judges which conversation history is still needed and the proxy omits validated spans before forwarding to Anthropic or OpenAI. Proof of concept, heading into CompozyOS. Read: README, `docs/CONTEXT-OPTIMIZATION.md`, `docs/BENCHMARKS.md`, `docs/STICKY-VALIDATION.md`.

How they shape a judgment (their `focused-context-v11` lifecycle):
- **One candidate per call, many calls in flight.** One historical span of about 1,200 characters per Jev request, up to eight requests in parallel, instead of one large batch. Same conclusion as TypeSafe's citation cookbook and as our section 7: atomic decisions, small state.
- **State = task + constraints + candidate + bounded evidence.** Each request carries the current human task, earlier human constraints (deduplicated), the complete candidate span with its tool identity, and at most 1,800 characters of retained receipts ranked against the task. Long code blocks and catalogs are explicitly kept out of the evidence set.
- **Two Boolean questions, both must be low to act.** "Would a required fact or code quote be lost?" and "Would a binding user constraint be lost?"; the span is omitted only when both probabilities are at most 0.2. Earlier stages used 0.8 for a proposal and 0.95 for preservation. They call this an experimental operating point, not a calibration claim.
- **Fail closed, freeze decisions.** Invalid, failed or timed-out judgments keep the span; keep/omit/failed/skipped decisions are frozen and later turns only judge new arrivals; source hashes and exact offsets are rechecked before a verdict is applied. This is the same rule we wrote for the judge: it may only make things stricter, and unavailability changes nothing.
- **Receipts carry the cost.** Per-kind coverage, Jev request bytes, applied spans, Gateway cost and provider usage are recorded per request; missing receipts are reported as unknown, not estimated.

How they report results, which the batuta articles should copy:
- "Measured, not claimed": one trial per arm, hash-verified study registry, generated offline with no new model calls, and the headline sentence is negative where the data is negative ("Both Yoshi trials were slower than baseline", "There is no single validated savings percentage for Yoshi").
- Latency is reported as a cost of the design: judging on the request path took a session from 39.77 s to 210.54 s of wall time in one validation trial, with three Jev calls hitting the 45 s deadline. Their fix is architectural (judge between turns, not on the request path), which is exactly where batuta already sits: the loop judges after the gates, off any interactive path, so the 0.7 s per attempt we measured is not user-visible.

What batuta should take from it:
1. Keep v2's one-`choice`-per-claim design, and keep claims small (their 1,200-character spans; our claim plus its evidence slice).
2. Add the second axis they use: besides "is this claim contradicted", ask "would acting on this change the task outcome" only when we move to enforce; two low probabilities to act, never one high one.
3. Record cost per judgment in the journal (already done: `usage`, `latency_ns`, `provider`) and print unknown when a receipt is missing, never a guess.
4. Report the benchmark the way they do: per delivery, one trial per arm, negative headline if the data is negative.
5. Privacy note for the article and the docs: Jev-enabled judgments send executor output to TypeSafe (directly or through a gateway); our state builder already redacts paths and env-shaped lines, and the docs must say what leaves the machine, as yoshi's README does.

### 8.1 What yoshi's source adds (read 2026-09-21, `src/judge.ts`, `src/semantic.ts`)

- The state carries a `note` telling the model that candidate text is untrusted data, not instructions, and that absence from a preview is not proof of absence. Our state builder will carry the same note about the executor report.
- Questions point at state keys (`candidates.c1`, `` `request` ``, `` `retained_notes` ``) instead of restating content; several questions share one structured state.
- Acting requires positive evidence, asked as such ("Is there positive evidence that…"), and the criteria list what does not count as evidence ("Being old, long or cheap to reread is NOT evidence"). Two answers gate the action: `needed <= 0.2` and `safe >= 0.95` (legacy), or `conflict <= 0.2` with `covered >= 0.95` (atomic).
- Usage and gateway cost are collected per call; a missing usage is counted, never estimated; provider error messages are not logged because they may embed request content.

## 9. How others apply Jev, from GitHub (survey, 2026-09-21)

Search on 2026-09-21 over repositories mentioning Jev / TypeSafe System One returned about 40 active projects: SDKs (Rust, Ruby, Swift, Elixir), local Jev-compatible servers reading decisions from open-weight logits, MCP servers exposing Jev to agents, benchmarks (RAG, reward-model, frontier comparisons, a Russian-language calibration audit), and a handful of agent-safety tools. Three matter for batuta.

### 9.1 BargLabs/jev-judge-calibration — Jev as a judge of agent completion reports

A pre-registered, hash-verified study (`jev-judge-v1`, pinned `jev-1.13.0`, 450 cases, 900 calls, US$0.03) on the question batuta's `claim_evidence` asks: can Jev tell a false completion report from a true one? Corpus: 50 merged public pull requests as clean cases; four defect variants per case built by editing the report text only (`premature`, `fabricated_reference`, `wrong_count`, `scope_mismatch`), plus a persuasive rewrite of every defective report. Two conditions: `T` (report text only, one `noul` "Is this report an accurate description of completed work?") and `E` (report plus the evidence record as JSON, one `noul` "Does the report's description of the state of the work match the evidence?" and one `choice` over the five labels with criteria written to the letter of the construction rules). Threshold 0.5, frozen.

Results copied from `result/jev-judge-calibration-result-2026-09-20.md`:
- False-clear rate on plain defective reports, condition `E`: premature 90.0%, fabricated_reference 68.0%, wrong_count 84.0%, scope_mismatch 92.0%. Condition `T`: 62.0%–98.0%. A global `noul` clears most false reports.
- False-flag rate on clean reports: `T` 28.0%, `E` 0.0%.
- Calibration: ECE 0.458 (`T`) and 0.420 (`E`); "calibration not established" under their own gate. Noul values concentrate between 0.3 and 0.8 in both conditions.
- Adversarial shift: the persuasive rewrite lowered the mean noul (−0.129 in `T`, −0.198 in `E`), so persuasion made Jev more suspicious, not less.
- The `choice` over five defect labels in `E`: 374/430 = 87.0% correct against a 23.3% constant-answer baseline; per label, `accurate` 100%, `fabricated_reference` 99%, `premature` 48%.
- Their limit statement: one judge, one version, one wording per condition, constructed defects; it says nothing about triage, routing or extraction.

What this confirms for batuta, in order:
1. Our v1 (two global `noul` over the whole attempt) reproduced their `T`/`E` noul behaviour: probabilities compressed in 0.30–0.83 with weak separation. That is the documented failure mode of a global yes/no over a report, not a batuta-specific bug.
2. A `choice` with criteria "written to the letter of the construction rules" is the shape that worked (87% versus 23%). v2/v2.2's one `choice` per atomic claim with explicit criteria is the right direction; the criteria must name the concrete defect, not "contradicted" in the abstract.
3. Their thesis matches our v2.1 result: the false closures were caught by provenance/code (claimed path, unchanged tree), which a content judge cannot see. Code settles what code can settle; the judge classifies the residue.
4. Threshold discipline: freeze the wording and the threshold before the run, report contradicted predictions with the same prominence, never restate a result at a tuned threshold. Our benchmark file already states the rule before each run; keep doing that.

### 9.2 Agent-safety tools built on Jev

- `Brainwires/jevwire`: Jev decision layer for agents (MCP server, embeddable decision model, an "escalate-only" Claude hook). Same posture as batuta's rule: the judge may escalate, never approve.
- `celolopes/jev-dev-harness`: runtime safety toolkit for AI coding agents powered by Jev (gates on agent actions). Read for its gate catalogue when batuta reaches decisions 2–4 (environment-vs-genuine, usage limits, question triage).
- `Obrais-cloud/typesafe-mcp`, `Djancyp/oido-systemone`, `exfly/laya-jev-compatible-server`, `deepanwadhwa/OpenDecision`: Jev-shaped `/v1/systemone` servers over local models. Relevant later as a `provider` for offline judging: the `judge` package only needs a base URL.
- Numbers other projects use: `jevwire` gates agent actions with `thresholds: { auto: 0.85, review: 0.6 }` (block / confirm / allow) and its Claude Code hooks stay inactive without a key; `jev-dev-harness` falls back to regex and heuristics when offline ("your coding agents are never blocked by API downtime"), the same fail-closed posture as batuta's `ErrUnavailable`. The awesome list's field notes repeat two rules we already adopted: give uncertain cases somewhere to go (an "unknown" option; removing it forced wrong answers in a calibration audit) and audit the policy around the model, not the model alone.

## 10. Constructed corpus: decision rule, frozen before the run (2026-09-21)

Plan `judge-corpus` (delivery `judge-corpus-20260921-185659`) gave the judge a bounded diff slice as evidence, settled identifier and count claims in code, and added `batuta judge corpus build` and `batuta judge corpus run`. This section is written before the corpus is run and is not edited afterwards.

Corpus inputs, fixed:
- Binary built from `f2cc89c` (branch `feat/judge-corpus` after delivery `judge-corpus-fixes-20260921-213457`, whose final review returned SHIP with no findings).
- Core: every journal under `.batuta/journal/` except `judge-corpus-*` and `judge-corpus-fixes-*` (the deliveries that built the tool). Skills: every journal under `skills/.batuta/journal/`, built with the skills repository as workspace.
- Source cases: attempts whose recorded outcome is `candidate` and whose run log and diff resolve. Every other attempt is listed as skipped with its reason.
- Variants, exactly as specified in the plan's Task 3 decisions: `clean`, `fabricated_reference`, `wrong_count` (only when a changed `_test.go` file exists), `behaviour_absent`. Each variant appends one line to the unchanged report; paths are real changed paths.

Judge: `.batuta/judge.json` as committed on the run date (`provider: auto`, `claim_evidence` threshold 0.9). One trial. No wording, threshold or variant rule changes between build and run.

Rule:
- The judge earns its place on `claim_evidence` only if, at the configured threshold, it flags at least 50% of `behaviour_absent` cases and at most 5% of `clean` cases.
- Sanity for the code step: code flags at least 90% of `fabricated_reference` and `wrong_count` cases. If this fails, the corpus or the settlement is broken and the judge result is not read.
- Unavailable judge answers are counted separately and never as misses. If more than 10% of the judge's calls are unavailable, the run is repeated once and both runs are reported.
- A negative result is the headline. Nothing is restated at another threshold.

## 11. Plan classification: decision rule, frozen before the run (2026-09-22)

Agreed with the maintainer on 2026-09-22, before the bench was run. This section is not edited afterwards.

Inputs, fixed:
- Binary built from `7ba6ead`, the last commit of branch `feat/judge-classify` after delivery `judge-classify-fixes-20260922-112834`, whose final review returned SHIP with no findings.
- Tasks: every task of every plan under `core/.batuta/plans/done/` and `skills/.batuta/plans/done/` (175 tasks in 49 plans on 2026-09-21; the run uses whatever those directories hold when it starts, and records the count). The label is the lane the host wrote in the plan.
- Journals: `core/.batuta/journal/` and `skills/.batuta/journal/`, for measure (b) only.
- Judge: `provider: auto`, `classify` decision threshold 0.7 (the command default), from a config file passed with `--config`. One trial. No wording, threshold or criteria changes between build and run.

Measure (a) decides. The judge is fit to classify plan tasks only if, over all tasks, all three hold:
1. Exact complexity agreement with the label is at least 80%.
2. That agreement is at least 20 percentage points above the constant-answer baseline (the share of the most common label).
3. Under-routed tasks (judge lane lower than the label) are at most 10%.

Fallbacks to `critical` count as the judge's answer for agreement and routing direction. Unavailable answers are counted separately and excluded from the denominator; if more than 10% are unavailable, the run is repeated once and both runs are reported.

Measure (b) is reported beside it and decides nothing: for tasks with a journal, the first-attempt outcome of the executed lane against whether the judge's lane was lower, equal or higher.

A negative result is the headline. Nothing is restated at another threshold.

## 12. claim_evidence v3: decision rule, frozen before the run (2026-09-22)

Agreed with the maintainer on 2026-09-22, after the post-hoc reanalysis of run 1 and before any v3 corpus was built or run. This section is not edited afterwards.

Inputs, fixed:
- Binary built from `107c8cb`, the last commit of branch `feat/claim-evidence-v3` after delivery `claim-evidence-v3-20260922-123213`, whose final review returned SHIP with no findings.
- Corpus: `batuta judge corpus build` over the same journals as section 10 (every core journal except `judge-corpus-*` and `claim-evidence-v3-*`, the deliveries that built the corpus tooling; every skills journal), with the v2 labels: `clean`, `true_behaviour` (negatives); `fabricated_reference`, `wrong_count`, `behaviour_absent`, `wrong_diff` (positives). Split by the first byte of sha256 of `<delivery>/<task>/e<execution>`: even is calibrate, odd is test.
- Judge: `provider: auto`, `claim_evidence` aggregation v3 (a judge-answered claim flags when its contradicted probability reaches the threshold; no confidence or material gate).

Procedure:
1. Run `batuta judge corpus calibrate` once on the calibrate half. The threshold is the lowest of 0.50, 0.55 … 0.95 whose false-flag rate over `clean` and `true_behaviour` is at most 2%. If no threshold qualifies, the result is negative and the test half is not run.
2. Write that threshold into the judge config and run `batuta judge corpus run --split test` once.

Rule, on the test half:
- The judge earns its place on `claim_evidence` only if it flags at least 50% of `behaviour_absent` and `wrong_diff` cases together (claims code does not settle) and flags at most 5% of `clean` and `true_behaviour` cases together.
- Sanity: code flags at least 90% of `fabricated_reference` and `wrong_count`; if not, the corpus or settlement is broken and the judge result is not read.
- Unavailable answers are counted separately and excluded; above 10% unavailable, the run is repeated once and both runs are reported.
- A negative result is the headline. Nothing is restated at another threshold.

## 13. Plan classification v2: decision rule, frozen before the run (2026-09-26)

Agreed with the maintainer on 2026-09-26, before any v2 code was built. This section is not edited afterwards.

Why v2: run 1 (section 11, benchmark "Plan classification, run 1") measured a rubric whose `high` criterion ("subsystem or multi-file work fully captured by a precise brief") describes almost every batuta plan task, and it scored against lanes the host wrote rather than against what happened.

Rubric. Code measures what is objective; Jev answers only yes/no questions; a fixed rule maps both to a lane.
- Code, from the task's Scope: number of files; number of distinct directories (packages); test-only (every entry is a test file or under `testdata/`); docs-only (every entry is `*.md` or under `docs/`).
- Jev, one `noul` question each, from the task text and its plan context:
  - (a) contract: does it change a public or cross-package contract (exported API, CLI flag, file format, protocol)?
  - (b) lifecycle: does it involve concurrency, process lifecycle, I/O timing or retries?
  - (c) security: is it security-sensitive (permissions, sandbox, secrets, redaction)?
  - (d) open decision: does it depend on a decision the plan does not state?
  - (e) mechanical: is it purely mechanical (rename, copy, config, documentation wording)?
- An answer below confidence 0.7, or unavailable, counts as yes for (a)–(d) and no for (e): uncertainty only ever raises the lane.
- Mapping, first match wins: `critical` if (d); `low` if (e) and at most 2 Scope files and none of (a), (b), (c); `high` if (c), or at least two of (a), (b), (c), or at least 3 directories; otherwise `medium`.

Labels, from outcomes. For each plan task with a delivery journal, L is the lane of its first attempt and the outcome is the one `batuta judge classify` already derives: `candidate` or `retried` means sufficient at L; `escalated` or `failed` means insufficient at L; `unknown` (no journal) is excluded from the decision and counted.

Inputs, fixed:
- Binary built from the last commit of branch `feat/classify-v2` after its plan's final review returns SHIP.
- Tasks: every task of every plan under `core/.batuta/plans/done/` and `skills/.batuta/plans/done/` when the run starts; the count is recorded. Journals: `core/.batuta/journal/` and `skills/.batuta/journal/`.
- Judge: `provider: auto` (Jev), one trial, no wording, threshold or rule change between build and run.

Decision. v2 (lane J) is fit to propose lanes only if all four hold:
1. Economy: among sufficient tasks, J ≤ L for at least 75%.
2. Safety: among insufficient tasks, J > L for at least 50%. If fewer than 10 insufficient tasks exist, safety is reported and does not decide.
3. Discrimination: no lane holds more than 70% of J, and J uses at least 3 lanes.
4. Balance: (economy + safety) / 2 is at least 0.65 (the host's own lanes score 0.5: economy 1.0, safety 0.0).

Reported beside it, deciding nothing: agreement with the host lane, the v1 answers of run 1 for the same tasks, the distribution of (a)–(e), and unavailable answers (if more than 10% are unavailable, the run is repeated once and both are reported).

Shadow only: nothing routes on J. A negative result is the headline; nothing is restated at another threshold or mapping.

## 14. Plan classification v3: decision rule, frozen before the build (2026-09-27)

Agreed with the maintainer on 2026-09-27, before any v3 code was built. This section is not edited afterwards.

Why v3: v2 (section 13) asked Jev five yes/no questions about a task excerpt that does not contain what decides them, and its rule turned every answer below 0.7 into the highest lane (197 of 228 tasks `critical`, economy 11.9%). v3 measures first what code alone can do, then asks Jev only questions whose decisive fact is in the packet, with a way to abstain, and lets an uncertain answer change nothing. Background: `.batuta/judge-application-study.md`.

Known contamination, stated before the run. The study computed post hoc rates on all 171 labelled v2 tasks (for example Scope `Files ≥ 7`: 10/40 insufficient against 10/131). The feature family below was chosen with that knowledge, so the test half is not unseen by the people who wrote the rule. The parameters are chosen by the mechanical procedure below on the calibrate half only.

Corpus and labels, as section 13. Tasks: every task of every plan under `core/.batuta/plans/done/` and `skills/.batuta/plans/done/` when the run starts; the count is recorded. Journals: `core/.batuta/journal/` and `skills/.batuta/journal/`. L is the lane of the task's first attempt. `candidate` or `retried` means sufficient at L; `escalated` or `failed` means insufficient at L; `unknown` is excluded from the measures and counted.

Split. By plan, so the tasks of one plan stay together: the first byte of sha256 of the plan slug, even is calibrate, odd is test. On the v2 corpus this gives calibrate 43 plans (75 sufficient, 5 insufficient, 37 unknown) and test 36 plans (76 sufficient, 15 insufficient, 20 unknown); only label counts were read to write this line. Five insufficient tasks make the calibrate selection noisy; that is accepted and not worked around.

Lane C, code only. From the task's Scope and text, first match wins:
1. `critical` if the open marker is present: the task title, its Accept entries or its own context paragraph contain, case-insensitive, one of `TBD`, `to be decided`, `to be defined`, `undecided`, `open question`, `open decision`, `decide whether`, or the task title contains `?`.
2. `high` if Scope files ≥ F_high, or Scope directories ≥ 3.
3. `low` if Scope files ≤ F_low, and the task is not docs-only unless docs_low is yes.
4. otherwise `medium`.

Grid for C: F_high in {4, 5, 6, 7, 8}; F_low in {1, 2}; docs_low in {no, yes}. Twenty points.

Lane J, C adjusted by Jev. Code builds a packet per question; a question whose packet is empty is not asked and counts as `insufficient`.
- `contract`. Packet: the exported identifiers (functions, methods, types, package-level variables and constants) and the flag names registered in the task's Scope files that are non-test `.go` files present in the plan's repository at the bench commit, sorted, at most 60. Question, `choice`: `exported_change` (the task text names, or directly describes changing, adding or removing, an identifier or flag in the packet, or a file format or protocol); `internal_only` (the task text describes a change that leaves every identifier and flag in the packet as it is); `insufficient` (the task text does not say enough to choose).
- `security`. Packet: the Scope entries whose path contains, case-insensitive, one of `permission`, `sandbox`, `secret`, `redact`, `auth`, `grant`, `contain`. Question, `choice`: `security_behaviour` (the task text asks to change what is permitted, contained, redacted or kept secret in a listed path); `incidental` (the task text touches a listed path without changing what is permitted, contained, redacted or kept secret); `insufficient`.
- An answer is firm when its option is not `insufficient` and its confidence is at least T. Anything else, including an unavailable answer, changes nothing.
- Mapping: J starts at C. A `critical` C is never changed. A firm `exported_change` or a firm `security_behaviour` raises J one lane, never above `high`. Otherwise, when at least one question was asked and every asked question is firm on `internal_only` or `incidental`, J drops one lane, never below `low`.

Grid for T: {0.7, 0.8, 0.9}.

Known limit of the contract packet: the tree is the one at the bench commit, so identifiers a finished task added are in its own packet. The packet tests whether Jev matches text to a list, not whether it predicts a change.

Procedure, in this order, each step recorded before the next starts:
1. Calibrate run: `batuta judge classify bench --rubric v3 --split calibrate --json` with the default rule; one Jev call per task that has at least one packet; every answer recorded with its full distribution.
2. Selection, offline, from the recorded calibrate run (`batuta judge classify calibrate`): for C, among grid points where no lane holds more than 70% of C and C uses at least 3 lanes, the highest balance; ties go to the higher economy, then the lower F_high, then the lower F_low, then docs_low no. If no point passes discrimination, the highest balance is taken and the failure is reported. For T, with C fixed, the highest balance of J; ties go to the higher T.
3. Freeze: the selected F_high, F_low, docs_low and T are committed as `.batuta/judge-classify-v3/rule.json` before the test half is read.
4. Test run: `batuta judge classify bench --rubric v3 --split test --rule .batuta/judge-classify-v3/rule.json --json`, one trial.

Inputs, fixed. Binary built from the last commit of branch `feat/classify-v3` after its plan's final review returns SHIP. Judge: `provider: typesafe`, `model: jev-1.13.0`, `timeout_ms: 20000`; the model is pinned, so the `auto` chain is not used. If more than 10% of the asked questions are unavailable, the run is repeated once and both are reported. No wording, grid, threshold or mapping change between build and run.

Decision, on the test half only, with the four measures of section 13 (economy at least 75%, safety at least 50%, no lane above 70% and at least 3 lanes, balance at least 0.65):
- C is fit to propose lanes only if all four hold for C.
- Jev adds to classification only if all four hold for J and balance(J) − balance(C) is at least 0.05.
- If Jev does not add, classification stays code-only and the next Jev decision is verifier-objection triage.

Reported beside it, deciding nothing: the calibrate table of every grid point; per question, the tasks with a packet, the calls made, and the answers firm, `insufficient`, below T and unavailable; the open marker count; agreement of C and of J with the host lane; input tokens.

Shadow only: nothing routes on C or J. A negative result is the headline; nothing is restated at another grid, threshold or mapping.

## 15. Question-to-plan matching: decision rule, frozen before the build (2026-10-01)

Agreed with the maintainer on 2026-10-01, before any code was built. This section is not edited afterwards.

Why. An executor's `BATUTA-QUESTION` parks its delivery until a person answers. The journals on this machine hold 85 such questions (core 45, menuflix 37, skills 1, host 1, core-soft-deny 1) and 41 recorded answers. Read by the conductor on 2026-09-30, most questions are of three shapes the plan already decides by structure: a request to widen Scope, an environment the executor cannot use, or permission to continue past a stop condition. The residue asks something the plan text may or may not state. Code classifies the shapes; Jev is asked, only on the residue, whether the task's own passage of the plan states the answer. Doctrine: `.batuta/judge-application-study.md`, sections 4 and 5. Verifier-objection triage was parked on 2026-09-30 for lack of positives (`.batuta/judge-benchmark.md`, "Verifier-objection triage: data check").

Known contamination, stated before the run. The word lists and the order below were written after the conductor read all 85 questions on 2026-09-30, so criterion 1 measures the fit of lists tuned on the same questions they are scored on; the labels are the maintainer's, not the conductor's alone, and new questions recorded after this date are the only unseen ones.

Corpus. Every `question_recorded` record of every journal under the five directories (`core`, `skills`, `batuta`, `core-soft-deny` and `geeknaveia/menuflix`, each `.batuta/journal/`) when the run starts; the count is recorded. Each question is joined to its plan by the delivery's slug (`<workspace>/.batuta/plans/<slug>.md` or `plans/done/<slug>.md`) and its task; a question whose plan or task is not found is counted and excluded. The `answer_recorded` text of the same task and execution, when present, travels with the question and is never shown to the judge.

Split. By delivery: the first byte of sha256 of the delivery name, even is calibrate, odd is test. Reported, deciding nothing: with 85 questions the halves are too small to select a threshold, so the threshold is fixed below and the split only shows whether the counts hold on both halves.

Labels, by the maintainer after reviewing labels the conductor proposes, frozen in `.batuta/judge-questions/labels.tsv` before the bench runs. Two per question:
- `kind`: `scope_change` (asks to touch or add a path outside the task's Scope, or to install a dependency), `environment` (the executor cannot run or reach something: sandbox, permissions, Docker, network, caches, disk), `continue` (asks leave to go on after a stop condition, a failure or a timeout, with no other change), `other`.
- `answer_in_passage`: `yes` when a sentence of the task's passage (defined below) states the answer or the rule that decides it, `no` when none does, `unclear` when the question itself is too unclear to say.

Code kind. From the question text, case-insensitive, first match in this order:
1. `environment` if it contains one of: `sandbox`, `docker`, `colima`, `socket`, `not permitted`, `permission`, `gocache`, `network`, `loopback`, `/bin/ps`, `write access`, `connectivity`, `container`, `disk`, `disco`, `ambiente`, `environment`, `toolchain`.
2. `scope_change` if it contains `scope` or `escopo`, or `install`, `instalar`, `dependenc` (English or Portuguese stem), or names a path outside the task's Scope: a token with a `/` and one of the extensions `.go .ts .tsx .js .php .md .json .yaml .yml .lock .sh`, not equal to and not under any Scope entry.
3. `continue` if it contains one of: `stop condition`, `continue past`, `may i continue`, `may i resume`, `posso continuar`, `posso retomar`, `pode retomar`, `nova tentativa`, `retry`, `resume this`, `retomar est`, `resume verification`, `continue verification`.
4. otherwise `other`.

Passage. The task's title, its Scope and Accept entries, its own labelled paragraph of `## Decisions and context` and the unlabelled paragraphs, in that order, bounded to 4000 bytes and with secret-shaped lines dropped, as `classify` bounds a task's context. The host lane never travels.

Jev, on `other` questions only. One `choice` question over the packet `{question, passage}`: `answered_here` (a sentence of the passage states what the question asks, or states the rule that decides it); `not_addressed` (no sentence of the passage addresses what the question asks); `insufficient` (the question or the passage is too unclear to choose). An answer is firm when its option is not `insufficient` and its confidence is at least 0.9, the maker's band for automatic action. Anything else changes nothing. Model `jev-1.13.0`, `provider: typesafe`, `timeout_ms: 20000`, one trial; if more than 10% of the calls are unavailable the run is repeated once and both are reported.

Known limit, stated before the run. `not_addressed` is a statement about absence, which the study says Jev does badly. It is asked here only because the passage is whole and small, not a slice of something larger, and the decision below never acts on it: only a firm `answered_here` can count.

Decision, on the whole corpus, counts and shares both recorded:
1. Code kinds are fit to route questions if, for each of `scope_change`, `environment` and `continue`, at least 90% of the questions code puts in that kind carry that label (precision), and at least 70% of the questions labelled with that kind are put there by code (recall).
2. Jev adds if, among `other` questions labelled `yes`, at least half get a firm `answered_here`, and among `other` questions labelled `no` or `unclear`, none does. If fewer than 6 `other` questions are labelled `yes`, criterion 2 is reported and does not decide, and the decision on Jev is "no data", not negative.
3. If 1 holds and 2 does not, question routing ships code-only in shadow (an annotation on the ask file), and Jev's next candidate is finding-to-contract consistency.

Reported beside it, deciding nothing: the confusion table of code kind against label; per split the same counts; for every `other` question the option, confidence and probabilities; agreement of a firm `answered_here` with a recorded answer that repeats the passage; input tokens and calls.

Shadow only: nothing answers a question on any of this. A negative result is the headline; nothing is restated at another threshold, word list or mapping.

## 16. Jev positive control: decision rule, frozen before the run (2026-10-02)

Agreed with the maintainer on 2026-10-02, after the question-matching run and before any control call. This section is not edited afterwards.

Why. Four decisions (claim evidence, classification v1–v3, question matching) were negative under frozen rules, and in every one Jev's probabilities failed to separate the labelled classes while its escape option went unused. None of those runs included a positive control: a set of packets so plain that a working judge must get them right. Without it the negative results cannot be attributed to the domain rather than to the request format batuta sends. This control settles that.

Items. Forty packets in `.batuta/judge-control/items.json`, written by the conductor on 2026-10-02, English, each passage under 200 bytes, in the exact request shape of question matching (`judge ask` with the state `{question, passage}` and the single `choice` question `answer` whose instructions and three options are the ones of `questions/request.go`). Two tiers of twenty: `literal`, where the passage names the question's subject and decides it (ten `answered_here`) or never mentions it (ten `not_addressed`); `one_hop`, where a general rule in the passage decides the question without naming its subject (ten `answered_here`) or the passage holds unrelated rules (ten `not_addressed`). No item expects `insufficient`.

Judge: `provider: typesafe`, `model: jev-1.13.0`, `timeout_ms: 20000`, one trial, one call per item through the maintainer's login shell.

Decision, per tier and overall, with the threshold 0.9 of section 15:
1. The request format is sound if, on the `literal` tier, at least 18 of 20 answers are firm (confidence at least 0.9) and correct, and no firm answer is wrong.
2. One-hop reading works if the same holds on the `one_hop` tier.
3. If 1 fails, the request format or the question wording is the first suspect, and the four earlier negatives are not attributed to the domain until the format is fixed and this control passes. If 1 holds and 2 fails, Jev reads literal statements but not one-hop rules on this kind of text, and the earlier negatives stand as domain results.

Reported beside it, deciding nothing: every item's option, confidence and probabilities; the lowest P(expected option) among correct items and the highest among wrong ones; input tokens.

Shadow only. A negative result is the headline; nothing is restated at another threshold.
