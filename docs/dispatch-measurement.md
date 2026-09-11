# Dispatch measurement protocol

This protocol measures whether compact dispatch reduces orchestration cost
without weakening acceptance. It defines comparable observations; it does not
contain pilot results or a savings verdict. The host release plan owns the
actual paired pilot, qualification decision and rollout.

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
