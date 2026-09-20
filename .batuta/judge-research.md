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
| Vercel AI Gateway | `typesafe-ai/jev` | Only documented through AI SDK 7 `experimental_evaluate`; raw HTTP shape not published | `AI_GATEWAY_API_KEY` | Yes/no type is `boolean`, not `noul`. Free until 2026-09-25 with very tight throttling (429 after a few questions); price after that not published. Unverified for a Go client. |
| opencode | `opencode/jev-1.13-free` | chat wrapper | — | **Does not work.** Probe on 2026-09-20: `Error: Upstream request failed: Endpoint is unavailable.` opencode speaks chat completions; Jev only answers the decisions API. Not a viable route. |

Recommendation: implement the native TypeSafe shape once in Go (`net/http`, standard library) and make the base URL and model id configurable; OpenRouter is the same body with another URL and model id. Treat Vercel as a later transport once its HTTP contract is documented.

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
