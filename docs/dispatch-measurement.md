# Dispatch measurement protocol

This protocol measures whether compact dispatch reduces orchestration cost
without weakening acceptance. It defines comparable observations and records
one native qualification below; it does not contain matched pilot results or
a savings verdict. The host release plan owns the actual paired pilot and rollout.

## Native OpenCode qualification

Release-owner review accepted four native cases on **2026-09-17**, using
macOS **26.6.2**, `darwin/arm64`, Go **1.26.4**, and native constructor commit
`76da07454db4612c9cb882ff328b055ade84c23a`. The exact candidate was executor
`opencode`, fixed argv `opencode acp`, version **1.18.31**, model
`opencode/big-pickle`, empty effort, and model config ID `model`.
The provider executable SHA-256 was
`16c960ba77421da11b53e785f359b73f328a86118b48feb4af143db5d9afb198`.
The constructor now supplies only this qualification.

The probes called the production constructor's ACP backend directly, bypassing
only the then-absent qualification record. The exact version was checked
separately. They retained the constructor's reject callback and managed-group
shutdown. They did not exercise outer dispatch selection; injected gate tests
cover selection. A separate real stock `batuta dispatch` smoke passed with
backend `acp`, exit class `completed`, exit code 0, and exact file contents
verified independently. It took **10,456 ms** and used candidate binary SHA-256
`95b884c7bcbaa072e2e800fea66712895e825a676c6b39da1a23cc8ee4ea5695`.
This was an additional sixth real attempt beyond the five qualification probes.

To reproduce the qualification, pin that constructor and provider, verify the
version and binary hash, and run each case once in a fresh disposable workspace
on the native platform. Use a 60-second task timeout and 90-second wall limit.
Keep existing provider permissions for task/cancel/deadline; tighten only the
permission case with project-local edit/bash `ask`. Count rejected callbacks
by wrapping the original policy without changing its decision. Verify actual
filesystem contents, child identity and shutdown rather than model prose.

| Accepted case | Required observation and result | Duration / shutdown |
|---|---|---|
| Task | `artifact.txt` contained exactly `batuta-native-acp-ok` and one newline; submitted, completed, worker success | 29,385 / 67 ms |
| Permission | One callback rejected; `denied.txt` absent; `permission_denied`, uncertain submission, unknown worker | 4,918 / 160 ms |
| Running-child cancellation | Live marked child identified before context cancellation; uncertain submission, canceled transport, unknown worker; child separately observed absent afterward | 18,819 / 79 ms |
| Deadline | Live marked child observed through 59,905 ms; 60-second execution deadline; uncertain submission, timeout, unknown worker; child separately observed absent afterward | 60,400 / 89 ms |

For cancel/deadline, use a foreground shell child with a 120-second independent
lifetime, a startup marker, and an expiry marker. Interrupt only after verifying
the marked child's live identity; verify absence afterward and ensure expiry
did not cause the result. All four cases verified managed-group disappearance
and direct-child reaping. This is the managed-group contract, not containment
of arbitrary escaped descendants; discovered PIDs are never individual signal
targets.

There were **five real qualification attempts**, including one inconclusive
initial cancel probe. That probe completed without an observed live child, so
it proved neither in-flight cancellation nor child absence. Ambiguous command
punctuation was a plausible explanation, not a proven provider cause. After
verified cleanup, a deliberately clarified command in a fresh fixture supplied
the accepted cancellation evidence. Both attempts remain in the evidence; no
uncertain task was replayed. The corrected probe source
SHA-256 was `11b5604b45ffb05b375d7711d5bd505ecff12dc59333d2fed350bf741e048087`.

Among the qualification probes, only the task and inconclusive cancel attempt
reported worker usage, with provenance `acp/session-prompt/usage (draft)`.
Their raw input/cached-input/output counters were respectively
**325 / 22,272 / 64** and **184 / 19,200 / 12**.
The separate stock-dispatch smoke reported **175 / 19,712 / 36** with the same
provenance. Cached input exceeds reported input in these observations; retain
the raw fields without inferring normalized totals or billing semantics. Usage for
permission rejection, accepted cancellation and deadline is **unknown**, not
zero. Conductor usage, reasoning counters, subscription quota, API spend and
independently verified effective model/billing were not established.

These are functional qualification results, **not a token-savings result**.
There was no matched CLI/native-host baseline, no completed matched-pair
cohort, and no aggregate consumption estimate. The protocol below still
governs any future pilot; qualification does not satisfy its acceptance targets.

## Cohorts

Use three bounded representative tasks:

1. read-only localization;
2. a small regression fix;
3. independent delivery review.

For every comparable pair, pin the same repository snapshot, task/brief,
acceptance criteria and proof commands, executor version, selected model and
effort, tool permissions, timeout and context contract. Record the snapshot
commit and a digest of the brief and criteria. A native child or external run
with a different model, inherited context or permissions belongs to a separate
cohort and is not evidence of a transport effect.

Capture these stages where they are actually available:

- legacy CLI baseline;
- CLI with the compact context and receipt contract;
- native host dispatch with equivalent isolation and routing;
- ACP with the same compact contract.

Collect at least three completed matched pairs for each comparable backend.
Keep failed, canceled, rate-limited and uncertain attempts in the dataset:
their usage, latency and retries are real consumption even though they do not
count toward the minimum completed pairs.

## Observation record

Store raw counters and their provenance, not only derived totals. One record
per task includes:

| Group | Required fields |
|---|---|
| Identity | cohort, pair, task, snapshot commit, brief/criteria digest, run and attempt IDs |
| Route | executor and version, backend (`native`, `cli`, `acp`), requested model/effort, observed model/effort when independently available |
| Controls | permissions, isolation/context mode, timeouts and qualification evidence version |
| Conductor | input, cached input, output and reasoning tokens when reported; source/provenance for each family |
| Worker | receipt input, cached input and output counters plus `usage.provenance`; subscription quota and API spend separately when known |
| Attempts | initial attempt, every retry or fallback, exit class, uncertain state and why it ran |
| Outcome | every acceptance criterion and gate, accepted/rejected, latency, permission result and verified cleanup result |
| Estimates | context bytes or estimated tokens, estimator/version and assumptions, stored outside measured provider counters |

Never merge conductor and worker usage into one unlabeled number. Cached input
is a subset of input, not an additional token category. For a source, additive
tokens are `input + output`, plus reasoning only when that provider documents
it as a separate additive counter. If either additive counter is missing, that
source total is unknown. Missing and unsupported counters remain `unknown`,
never `0`; an explicit reported zero remains zero.

Foreground supervision is a separate measurement family. Passive journal
observation makes zero model calls: record poll count, bytes read and written,
notification attempts, elapsed time, and available host CPU or memory data as
observation overhead. Keep nullable intervention model counters in their own
fields, separate from both observation and delivery worker usage. A fixed
policy answer does not itself create token usage; a worker resumed afterward is
another worker attempt. Record observation, intervention, retries, and elapsed
time independently so retry or wait overhead cannot be mistaken for model
usage. Do not infer token savings from elapsed time, context bytes, or the
absence of counters. See
[loop-supervision.md](loop-supervision.md) for the runtime contract.

Sum consumption across all attempts before deriving per-task values. A retry
is a separate attempt, not free work. Keep provider-reported values distinct
from byte-derived or tokenizer-derived estimates. Estimates can support a
context-size comparison when exact host usage is unavailable, but they cannot
be relabeled as measured tokens, provider billing or subscription quota.

## Comparison and decision boundary

Compare accepted tasks only for the primary matched-pair medians, while also
reporting failures, retries and uncertain attempts for each cohort. The
approved review targets are:

- at least 30% lower median measured conductor tokens per accepted task than
  the legacy CLI baseline;
- no failed acceptance criteria;
- no more than 5% growth in median total measured tokens.

These are evaluation targets, not claims or guarantees. Total measured tokens
must retain its conductor/worker breakdown and include all attempts; cached
tokens are not double-counted. Report latency and permission/cleanup outcomes
alongside token results. If exact conductor usage is unavailable, publish only
a labeled context-size comparison and mark the token-savings decision
inconclusive.

Do not add a performance dashboard or another LLM to summarize logs. Preserve
the raw observation records, compute the small set of aggregates
deterministically, and review exceptions against the receipts and bounded
artifacts. Transport alone does not change provider billing.

## Rollback during measurement

A failed qualification or unfavorable cohort disables ACP only for future
attempts of that exact executor/version/model/effort/platform combination.
Return those future attempts to explicit `cli`; do not reinterpret unlike
cohorts as matched observations.

Rollback never authorizes replay of an ACP attempt that may already have
submitted its prompt. Preserve its receipt, artifacts and workspace, mark the
observation uncertain, and reconcile the work before deciding whether a new
CLI attempt is safe. The cost of that uncertain attempt remains in the cohort.
