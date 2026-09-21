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
  "timeout_ms": 3000,
  "max_state_bytes": 100000,
  "decisions": {
    "claim_evidence": {"mode": "shadow", "threshold": 0.9}
  }
}
```

`provider` is required: `typesafe`, `openrouter`, `vercel`, `"auto"` or `off`.
`model` is required except with `auto`; defaults per provider below.
`base_url` overrides the provider endpoint. `key_env` names the environment
variable that holds the API key (defaults per provider below). `timeout_ms` is
bounded to 500–30 000. `max_state_bytes` bounds the serialized state to
1 000–204 800 bytes; a larger state is refused, not truncated. `decisions`
names the mode (`off`, `shadow`, `enforce`) and confidence threshold (0–1) per
decision point; an unconfigured decision is off.

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

| Route | Model id | Endpoint | Key |
|---|---|---|---|
| `typesafe` | `jev-latest` | `POST https://api.typesafe.ai/v1/systemone` | `TYPESAFE_API_KEY` |
| `openrouter` | `typesafe/jev-1.13` | `POST https://openrouter.ai/api/alpha/decisions` | `OPENROUTER_API_KEY` |
| `vercel` | `typesafe-ai/jev` | `POST https://ai-gateway.vercel.sh/typesafe/v1/systemone` | `AI_GATEWAY_API_KEY` |

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
indented JSON on stdout:

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
works."}}` over the state `"connection check"` — printing the answer the same
way. With `"auto"` it also prints the answering provider on the success line
(`provider: typesafe`). It is the cheapest way to confirm that a provider,
model and key work before wiring a decision point.

Exit codes: `0` answered, `2` unavailable — the reason is printed on stderr —
and `1` usage or config error. Exit `2` is a non-failure outcome: the caller
keeps the deterministic rule.

Every question is also recorded as a `judge_intent` record before the call and
a `judge_result` record after it, carrying the decision name, the question
keys, the state digest (`sha256:<hex>` over canonical JSON), the model, the
answers, the token usage and the latency. The trace never carries the state
body — it may hold executor output — only its digest, so a run can be replayed
from the logs.

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
a failure. The state is built by code from bounded evidence: the task (id,
title, scope), criteria with proof verdicts, the last 60 lines of the
executor report (capped at 8 KiB), progress events, whether the tree
changed and which paths, the verifier signal and detail when present, and
the deterministic outcome. Executor output is untrusted input; secrets and
absolute paths are redacted before the judge sees them. The two questions
are `noul`:

- `claim_unsupported` — the executor's report claims work that the tree,
  proofs or verifier do not show.
- `verifier_contradicted` — a verifier DONE line is contradicted by a
  failed proof or by the executor's own report.

There is no confidence on `noul`; each probability is gated on the
decision's threshold (default 0.9). `shadow` records `judge_intent` and
`judge_result` (state digest and answers, never the state body) and
changes nothing. `enforce` may only fail a passing attempt: when either
probability is at or above the threshold it sets `report.Passed = false`,
appends a synthetic failing `judge` proof so `Failures()` and the trail
show the contradiction, and records blocker `claim_unsupported`. It never
turns a failure into a pass, never clears a gate, and never marks a task
satisfied. Any error, including `ErrUnavailable`, is recorded on
`judge_result` and ignored. The decision is not consulted when it is
`off`, the judge is off, or the attempt ended in a question, a rate
limit, an executor error or a reconciliation block.
