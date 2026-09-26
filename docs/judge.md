# `batuta judge` — decisions with a fail-closed fallback

`batuta judge` asks a System One decision model (TypeSafe Jev) typed questions
over a bounded state. Jev is not an LLM: it receives a `state` — a string,
object or array the caller has already reduced to the evidence that matters —
plus a map of typed questions, and returns typed answers with probabilities.
It does not generate text, cannot read a repository and cannot run commands.

The judge is optional infrastructure. With no configuration it is off, and
every caller keeps today's deterministic rule. Every failure — off, missing
key, timeout, rate limit, server error, malformed answer — surfaces as a typed
unavailable error, never as a partial verdict.

## The config file

The judge reads `.batuta/judge.json` under the workspace (the current
directory by default). A missing default file means the judge is off. The file
is capped at 4 KiB and unknown fields are rejected:

```json
{
  "provider": "typesafe",
  "model": "jev-latest",
  "base_url": "",
  "key_env": "TYPESAFE_API_KEY",
  "timeout_ms": 10000,
  "max_state_bytes": 100000,
  "decisions": {
    "claim_evidence": {"mode": "shadow", "threshold": 0.9}
  }
}
```

`provider` is required: `typesafe`, `openrouter`, `vercel`, `"auto"` or `off`.
`model` is optional for a single provider — each has a documented default (the
providers table below) — and still rejected with `auto`; an explicit `model`
always wins. `base_url` overrides the provider endpoint. `key_env` names the
environment variable that holds the API key (defaults per provider below).
`timeout_ms` defaults to 10000 and is bounded to 500–30 000; real providers
need the room on a cold connection. `max_state_bytes` bounds the serialized
state to 1 000–204 800 bytes; a larger state is refused, not truncated.
`decisions` names the mode (`off`, `shadow`, `enforce`) and confidence
threshold (0–1) per decision point; an unconfigured decision is off.

With `"auto"`, `model`, `key_env` and `base_url` are rejected: each provider
keeps its own defaults. An optional `providers` array names a subset and
fixes the order (`["typesafe","vercel","openrouter"]`); each name at most
once, only those three. Omitted, it is that default order — TypeSafe first
as the origin, then Vercel because it keeps TypeSafe's shape, then
OpenRouter. `auto` builds a chain from the providers whose key is present
and walks it on every call.

## Environment variables

- `BATUTA_JUDGE` — `off` turns the judge off everywhere; empty uses the
  default file; otherwise a path to another JSON file, absolute or relative
  to the workspace. It never carries a key.
- The API key lives only in the environment variable named by `key_env`:
  `TYPESAFE_API_KEY`, `OPENROUTER_API_KEY` or `AI_GATEWAY_API_KEY` by
  default. A missing key is unavailable, and the key never lands in a file, a
  trace record, a log or a test fixture.

## Providers

| Route | Default model | Endpoint | Key |
|---|---|---|---|
| `typesafe` | `jev-latest` | `POST https://api.typesafe.ai/v1/systemone` | `TYPESAFE_API_KEY` |
| `openrouter` | `typesafe/jev-1.13` | `POST https://openrouter.ai/api/alpha/decisions` | `OPENROUTER_API_KEY` |
| `vercel` | `typesafe-ai/jev` | `POST https://ai-gateway.vercel.sh/typesafe/v1/systemone` | `AI_GATEWAY_API_KEY` |

The model id in the table is the default a single-provider config gets when
`model` is omitted (and the model the HTTP judge falls back to); an explicit
`model` overrides it. With `auto`, each provider keeps the default from this
table.

The native shape posts `{state, model, questions}` and receives
`{model, answers, usage}`; every question in one call is evaluated against the
same state. OpenRouter wraps errors as `{"error":{...}}` and has a 32k
context. The Vercel AI Gateway serves Jev over raw HTTP in two shapes: a
TypeSafe-compatible API at base URL `https://ai-gateway.vercel.sh/typesafe`
(`POST /typesafe/v1/systemone`, TypeSafe's exact request body, TypeSafe's
response shape plus a `provider_metadata.gateway` object, errors as
`{"message":…,"error_type":…}`), and a gateway-native API at
`POST https://ai-gateway.vercel.sh/v1/evaluate` with `boolean` instead of
`noul` and camelCase `usage`. The judge uses the TypeSafe-compatible route
because it needs no second parser.

`auto` returns the first answer in the chain. It continues to the next
provider on `rate_limited`, `server_error`, `timeout`, `malformed_response`
and `key_missing`. Any other error — including `state_too_large` and
`answer_mismatch` — stops the chain at once. When every provider is
unavailable the error is `all_unavailable` and wraps each attempt's provider
and reason in order, reachable through `errors.As` as `ChainError`. With no
key present at all, `Judge()` returns `key_missing` naming
`TYPESAFE_API_KEY`, `AI_GATEWAY_API_KEY` and `OPENROUTER_API_KEY`. Trace
`judge_result` records name the provider that answered and every provider
that was skipped, with its reason.

## The CLI

`ask` sends one request built from files and prints the `Response` as
indented JSON on stdout, with the same snake_case keys the API answers in:
`model`, `answers`, `usage`, `input_tokens`, `output_tokens`.

```text
batuta judge ask --state-file <path> --questions-file <path>
                 [--decision <name>] [--config <path>]
                 [--workspace <dir>] [--base-url <url>]
```

`--state-file` holds the bounded state as JSON — a string, object or array.
`--questions-file` holds the questions map in the request shape, for example
`{"ok": {"type": "noul", "instructions": "…"}}`. `--decision` names the
decision for the trace records (default `manual`); `--config` overrides the
config path and wins over `BATUTA_JUDGE`; `--base-url` overrides the
configured provider endpoint.

`probe` validates the configuration and sends a single `noul` question —
`{"ok": {"type": "noul", "instructions": "The state says the connection
works."}}` over the state `"connection check"` — and prints one success line
with the answering provider, the model that answered, the latency measured
around the ask and the token usage:

```text
provider=typesafe model=jev-1.13.0 ms=812 tokens=12/3
```

It is the cheapest way to confirm that a provider, model and key work before
wiring a decision point.

`replay` judges a past delivery after the fact:

```text
batuta judge replay --journal <path> [--runs <dir>] [--config <path>]
                    [--decision <name>] [--json] [--workspace <dir>] [--base-url <url>]
```

`--journal` is a delivery journal (`.batuta/journal/<delivery>.jsonl`). For
each `gates_reported` record with a matching `executor_finished` record,
replay locates the attempt's run log `<runs>/<date>-<slug>-<task>-e<n>.out.log`
(`--runs` defaults to `<workspace>/.batuta/runs`; a relative `--runs` is
workspace-relative), rebuilds the attempt the way the loop decides live —
code extracts the atomic claims from the run log, settles what it can, and
asks one `choice` per unsettled claim in a single request — and prints one
line per attempt:

```text
task_1 e1 outcome=already_satisfied asked=true claims=3 code_contradicted=1 judge_contradicted=1 uncertain=1 max_contradicted=0.93 material_max=0.95 flagged=true provider=typesafe tokens=2481/37 ms=812
```

Beside the attempt's `outcome` and the answering `provider`, the line reports
the v3 breakdown: `claims` is the total number of extracted claims;
`code_contradicted` and `judge_contradicted` count the claims settled as
`contradicted` — the code ones, and the judge ones whose `contradicted`
probability reaches the decision threshold (default 0.9);
`uncertain` counts the judge answers that landed in the uncertain bucket (a
`contradicted` probability from 0.30 up to the threshold); `max_contradicted`
is the highest `contradicted` probability among the judge's answers (`0.00`
when the judge was not asked); `material_max` is the highest material
probability among the judged claims (`0.00` when none were asked);
`flagged` repeats the aggregation the loop applies. When the judge answered,
`tokens=<input>/<output>` is the answer's usage and `ms=` the latency measured
around the ask. `asked=false` marks an attempt with no unsettled claims:
nothing is sent and no tokens are spent, and the breakdown is code-only.

The outcome is the recorded verdict of the attempt: the `blocker` of the
following `failure_recorded` record (for example `already_satisfied`),
`candidate` for `candidate_recorded` or `question` for `question_recorded`.
The run log is not journaled, so an attempt whose log is missing is reported
as `task_1 e1 skipped <expected path>` — replay state is built from the
journal and the log only, so the plan's scope is empty, the criteria are
recovered from the recorded proof signals, the changed paths only from a
failed scope verdict, and the progress events carry no timestamps. The
command ends with a totals line — every attempt is counted, `asked` is how
many the judge answered and `skipped` how many lacked a run log:

```text
attempts=2 asked=1 skipped=1 input_tokens=12 output_tokens=3
```

With `--json`, the text lines are replaced by one JSON object per attempt —
the same fields as the text line plus the per-claim `claims` list (kind,
text, report line, source, choice, confidence, material) and the `uncertain`
list, and, when the judge answered, `model`, `input_tokens`, `output_tokens`
and `latency_ms`; a skipped attempt carries `task_id`, `execution` and
`skipped` with the missing path, an unavailable one the `unavailable`
reason — and a final `{"totals":{...}}` object holding
`attempts`, `asked`, `skipped`, `input_tokens` and `output_tokens`. Replay is
read-only: the journal and the run logs are opened for reading and never
through the journal's append paths. Exit `0` even when attempts are skipped
or a later ask fails (`unavailable=<reason>` on that line); exit `2` when the
judge is unavailable before the first question is answered, and `1` for
usage, config or journal errors.

Exit codes: `0` answered, `2` unavailable — the reason is printed on stderr —
and `1` usage or config error. Exit `2` is a non-failure outcome: the caller
keeps the deterministic rule.

### `corpus`

`corpus build`, `corpus run` and `corpus calibrate` measure the
`claim_evidence` pipeline on a constructed corpus: real legitimate attempts
plus report-only defect variants, so the judge's contribution is scored on
claims code cannot settle.

`corpus build` turns recorded delivery journals into corpus cases:

```text
batuta judge corpus build --journal <path> [--journal <path>...]
                          --out <file> [--runs <dir>] [--workspace <dir>]
```

Every recorded attempt whose outcome is `candidate` becomes one clean case;
report-only defect variants append exactly one false line to the unchanged
report, one case per label:

- `clean` — the attempt's own report (a negative);
- `true_behaviour` — an update claim the diff does carry out (a negative);
- `behaviour_absent` — an update claim the diff does not carry out;
- `fabricated_reference` — an added identifier that is not in the diff;
- `wrong_count` — an added-tests count above the real one;
- `wrong_diff` — the next delivery's diff with this attempt's report.

A variant exists only when the frozen rule names the material it needs — a
changed path, an added identifier, a different delivery to borrow a task
title or a diff from.

A case is one JSON line: the case id (`<delivery>/<task>/e<execution>/<label>`),
the delivery, task and execution it came from, the label, the split, the
bounded report the executor wrote, the candidate diff, the redacted changed
paths, the proof verdicts, the verifier verdict, and the SHA-256 of the
report and the diff.

The split is frozen at build time: the parity of the first byte of the
attempt id's SHA-256 puts every case of one attempt in the same half,
`calibrate` or `test`, so the test half never contains a case whose own
half chose the threshold. Attempts that cannot become cases — a
non-candidate outcome, a missing run log, an unresolved diff — are skipped
with the reason on stderr. The same journals always build the same bytes.

`corpus run` scores the pipeline on a built corpus:

```text
batuta judge corpus run --corpus <file> [--split calibrate|test] [--json]
                        [--config <path>] [--workspace <dir>] [--base-url <url>]
```

`--split` scores one half only, `calibrate` or `test`, and defaults to
scoring every case in the file; anything else is a usage error. The run
reuses the live loop's claim extraction, code settlement, request
building and aggregation — no scoring logic lives in the command. Code
settles what it can settle exactly against the case's changed paths, proof
verdicts and verifier lines; a case with unsettled claims gets exactly one
judge call whose state carries the claims with their evidence and a bounded
diff slice. The threshold is the `claim_evidence`
decision's configured threshold (default 0.9) and is printed; there is no
threshold flag. One line per case:

```text
threshold=0.9
d1/task_1/e1/behaviour_absent label=behaviour_absent flagged=true settled_by=judge max_contradicted=0.93 asked=true
```

`settled_by` names the source of the first contradicted claim — `code`,
`judge` or `none`; an uncertain judge answer never settles a claim. A case
counts as flagged when the aggregate is flagged. When the judge is
unavailable on a case the line carries `unavailable=<reason>`, the case
keeps its code-only verdict and the summary counts it separately, never as
missed. With `--json`, the run prints one JSON object per case (id, label,
split, flagged, settled_by, max_contradicted, asked, uncertain, unavailable)
and a final `summary` object.

The summary folds the cases into one row per label — cases, flagged by code,
flagged by judge, uncertain, missed for the defect labels, false flags for
`clean` — and a footer with the judge calls, the input tokens summed from
the responses' usage (`unknown` when no response carried usage) and the
unavailable total:

```text
label cases flagged_by_code flagged_by_judge uncertain missed false_flags unavailable
behaviour_absent 1 0 1 0 0 0 0
clean 2 0 0 0 0 0 1
fabricated_reference 1 1 0 0 0 0 0
judge calls=2 input_tokens=12 unavailable=1
```

What leaves the machine is what the live `claim_evidence` decision sends:
per asked case, the task id, the unsettled claims with their evidence, and a
bounded diff slice — never the corpus file's other cases, never a key (the
API key lives in the environment variable `key_env` names). The run is
read-only: it touches the corpus file and the judge endpoint, and writes
nothing.

#### `corpus calibrate`

`corpus calibrate` scores only the calibrate half of a built corpus and
sweeps the decision threshold, so the threshold is chosen on that half
before any test-half run judges it:

```text
batuta judge corpus calibrate --corpus <file> [--json] [--config <path>]
                              [--workspace <dir>] [--base-url <url>]
```

The cases are scored once — the same pipeline as `corpus run`, one judge
call per case with unsettled claims — and every threshold of the fixed
sweep 0.50, 0.55 … 0.95 is derived from the recorded contradicted
probabilities, so the sweep costs no further judge calls: a code-settled
contradiction flags at every threshold, and a case's max contradicted
probability flags at every threshold it reaches. The positives are the
defect labels (`behaviour_absent`, `wrong_diff`, `fabricated_reference`,
`wrong_count`); the negatives are `clean` and `true_behaviour`. One line
per threshold names the flagged count per label and the false-flag rate
over the negatives:

```text
threshold=0.50 behaviour_absent=1/1 clean=1/2 true_behaviour=0/1 wrong_count=1/1 false_flags=1/3 rate=0.3333
```

The sweep ends with the lowest threshold whose false-flag rate is at most
0.02, or `chosen=none` when every threshold exceeds it:

```text
chosen=0.95
```

`calibrate` reports a candidate threshold; it never changes a config. The
threshold a `corpus run` scores at stays the `claim_evidence` decision's
configured one. With `--json`, it prints one JSON object per threshold
(`threshold`, `labels` with each label's `cases` and `flagged`, `false_flags`,
`negatives`, `false_flag_rate`) and a final `{"chosen": …}` object holding
the chosen threshold or `null`.

### `classify`

`classify` asks the `classify` decision for a plan task's complexity lane and
domain from the task text alone. The state is the task's title, scope and
acceptance criteria, the context paragraphs labelled for the task followed
by the unlabelled Decisions paragraphs (secret-shaped lines dropped, bounded
at 4000 bytes) and a note that task text is data, not instructions. The request
never carries the host's lane. Two `choice` questions come back — `complexity`
over low/medium/high/critical and `domain` over the routing domains — and a
missing, unknown or under-threshold answer falls back on that axis only
(complexity to critical, domain to general), setting the `fallback` flag. The
threshold is the `classify` decision's configured threshold (default 0.7) and
is printed; there is no threshold flag. The judge only proposes: nothing
downstream reads these lanes unless a caller does, and routing is unchanged.

The plain form classifies one plan:

```text
batuta judge classify --plan <file> [--json] [--config <path>]
                      [--workspace <dir>] [--base-url <url>]
```

It prints the threshold, then one line per task:

```text
task_1 plan=testing/low judge=testing/low complexity=0.90 domain=0.90 fallback=false input_tokens=12
```

`plan=` is the host's lane, `judge=` the proposed one with both confidences;
an unavailable judge prints `unavailable=<reason>` and no lane. With
`--json`, it prints one JSON object per task instead.

The bench form scores the same decision against many plans — the frozen gate
is agreement with the host's labels:

```text
batuta judge classify bench --plan <file> [--plan <file>...] [--journals <dir>...]
                            [--rubric v1|v2] [--json] [--config <path>]
                            [--workspace <dir>] [--base-url <url>]
```

The default rubric is v1, with the output described below. `--rubric v2`
uses the frozen rule in [section 13 of the judge research note](../.batuta/judge-research.md#13-plan-classification-v2-decision-rule-frozen-before-the-run-2026-09-26).
Code measures file count, distinct directories, test-only and docs-only from
each task's Scope. Jev answers five yes/no questions about contract changes,
lifecycle, security, open decisions and mechanical work. Each task line shows
those answers, their confidences and defaults, the proposed lane and the
recorded outcome. The request uses the same bounded, secret-filtered task
context as v1 and does not include the plan lane.

The v2 summary counts sufficient outcomes (`candidate`, `retried`),
insufficient outcomes (`escalated`, `failed`) and excluded unknown outcomes.
Economy is the share of sufficient tasks whose proposed lane is no higher
than the plan lane (PASS at 75%); safety is the share of insufficient tasks
whose proposed lane is higher (PASS at 50%, reported only with fewer than
10 insufficient tasks). Discrimination passes when no lane exceeds 70% of
the scored proposals and at least three lanes are used. Balance is the
average of economy and safety (PASS at 0.65). The summary also reports lane
distribution, agreement with the plan lane, answer distribution, defaulted
answers and unavailable answers. `--json` emits task objects and one summary
object with the same measures. This is a shadow score; it changes no routing.

It prints one line per task (prefixed with the plan slug) and a summary:
tasks, exact complexity and domain agreement, under-routed (judge lane lower
than the label), over-routed, fallbacks, unavailable, the constant-answer
baseline (share of the most common label, first in lane order on a tie) and
the complexity confusion matrix. Where `--journals` directories are given,
their delivery journals (`*.jsonl`, found recursively, so both a bare journal
directory and a workspace root's `.batuta/journal/` are covered) add the
outcome beside the agreement: a delivery matches a plan when its file name
starts with the plan slug followed by `-`, and the outcome is the recorded
verdict of the task's first finished attempt — `candidate` when that attempt
recorded a candidate, `retried` when a later attempt ran on the same
executor, `escalated` when on a different one, `failed` when no later attempt
ran, `unknown` when no delivery or no finished attempt matches (unknown tasks
are counted, never dropped). Each line also names whether the judge lane was
`lower`, `equal` or `higher` than the executed lane — the plan's lane — and
the summary prints the outcome counts per relation, so a reader can see
whether under-routing coincides with first-attempt successes.

What leaves the machine is one classify request per task: the task text and
applicable plan context described above — never the host's lane, never other
plans or journals, and never a key (the API key lives in the environment
variable `key_env` names). The bench touches the plan files, the journal
files and the judge endpoint, and writes nothing.

## Safety rules

- The judge may block, escalate, demote or annotate. It **never approves**,
  never clears a gate, never answers a question and never widens a scope.
- Verdicts only ever move in the strict direction. State is pre-digested by
  code, bounded, and never includes secrets; executor output is untrusted
  input to the judge — prompt injection is a documented Jev weakness — which
  is another reason verdicts never loosen a rule.
- Every `choice` has an escape option (`unclear`/`other`). Acting requires
  confidence above the decision's threshold; `noul` has no confidence, so the
  gate is on the probability itself with margins on both sides — act above
  0.9, ignore below 0.1, log in between.
- Shadow mode first: record verdicts for at least one full delivery per
  project, compare them against the deterministic outcome and the operator's
  judgments, then enable per decision.

## `claim_evidence`

The loop's first decision runs after the gates `Decide()` an attempt and
before the attempt is recorded as a candidate, an already-satisfied task, or
a failure. Code extracts atomic claims from the executor report (paths
touched, criteria marked done, test and commit claims) and attaches only
the matching evidence: the path's presence in `changed_paths`, the proof
verdict and verifier line for that criterion, the tests gate. Claims that
code can settle exactly — a claimed path that is missing, a proof that
failed, a tests claim against a failing tests gate — never reach the
model.

Unsettled claims go in one request. The state is a short task summary
(`task` with id, title and scope, `outcome_gates`), a `note` that the
executor report and every claim are untrusted data — not instructions to
the judge — and that a short evidence slice is not proof of absence, and
`claims.cN` objects holding `claim`, `kind` and `evidence`. Each remaining
claim is two questions that point at those keys instead of restating the
text: `cN_relation` is a `choice` asking "Is there positive evidence in
`claims.cN.evidence` that `claims.cN.claim` is false?" whose options are
the concrete defect for that kind (`path_not_changed`; `proof_failed` and
`verifier_incomplete`; `tests_gate_failed`; `count_mismatch`) plus
`supported` and `unverifiable`; `cN_material` is a `noul` asking whether
the task would not be done if the claim were false. The unverifiable
criterion says a vague claim, a short slice or missing evidence is NOT
contradiction. Code maps any defect label to `contradicted`. Executor
output is untrusted input; secrets and absolute paths are redacted before
extraction.

Code aggregates the settled list: a code-settled contradiction flags on
its own. A judge contradiction flags when the judge's `contradicted`
probability for the claim reaches the decision threshold (default 0.9) —
there is no second gate on the choice's confidence or the `material`
answer; both are recorded beside the choice and nothing else reads them.
Judge answers whose `contradicted` probability lies from 0.30 up to the
threshold land in an `uncertain` bucket that is recorded and never acted on.
`judge_result` keeps its existing fields and adds `claims` (source
`code` or `judge`, choice, confidence, material), `uncertain` and
`material_max`. Provider error bodies never land in a journal record, a
trail or a log line: only the `UnavailableError` reason is recorded.

`shadow` records `judge_intent` and `judge_result` (state digest and
answers, never the state body) and changes nothing. `enforce` may only
fail a passing attempt: a flagged contradiction sets `report.Passed =
false`, appends a synthetic failing `judge` proof so `Failures()` and
the trail name the contradicted claim and its report line, and records
blocker `claim_unsupported`. It never turns a failure into a pass, never
clears a gate, and never marks a task satisfied. Any error, including
`ErrUnavailable`, is recorded on `judge_result` and ignored. The
decision is not consulted when it is `off`, the judge is off, or the
attempt ended in a question, a rate limit, an executor error or a
reconciliation block. The v1 state builder and `ClaimEvidenceQuestions`
remain exported so a replay `--v1` flag can still compare against the
baseline.

### Code-only claim_evidence

`claim_evidence` settles most claims in code — against changed paths, proof
verdicts and the tests gate — and that code path needs no provider. A config
with `"provider": "off"` now keeps and validates its `decisions`, so the
decision can run on code settlement alone while the judge stays unavailable
(`judge_off`):

```json
{"provider":"off","decisions":{"claim_evidence":{"mode":"enforce","threshold":0.9}}}
```

In this mode no request leaves the machine: the judge is never built and
every claim is settled from evidence already on disk. The kill switch still
wins — `BATUTA_JUDGE=off` ignores the file and turns every decision off.
The corpus result that motivated this mode: in the constructed corpus
(`.batuta/judge-benchmark.md`, "Constructed corpus, run 1") code settled
112/112 fabricated identifiers and 92/92 wrong test counts, while the judge
flagged 0/112 `behaviour_absent` cases; the 2/2 false closures of the v2.3
replay were also caught by code settlement.
