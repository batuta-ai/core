# Dispatch transports

Batuta has two distinct dispatch boundaries:

- **Native host dispatch** is an interactive host creating one of its own
  isolated child agents. Core cannot discover or invoke that capability, and
  `native` is not a valid `batuta dispatch` or `batuta loop` transport.
- **External dispatch** is core running an executor adapter through its legacy
  CLI or a qualified Agent Client Protocol (ACP) session. ACP is a transport
  for the same selected executor, model and effort; it is not evidence that
  the executor supports nested subagents.

An interactive host may choose a compatible native child itself. Otherwise it
can call the bounded external command. The headless loop uses only the external
path. In both cases, the worker's result is a claim: tree, scope, tests, proofs
and the independent verifier remain separate acceptance gates.

`--transport` controls `batuta dispatch` and loop task attempts. The separate
`batuta review` cohort driver continues to use its CLI adapters; it is not
silently converted to ACP. The loop's task verifier likewise remains an
independent CLI session even when the task attempt used ACP.

## Bounded external command

`batuta dispatch` performs exactly one attempt. It does not retry, resume,
install an executor, discover models or run acceptance gates.

```text
batuta dispatch \
  --brief-file /absolute/path/task-11.brief.md \
  --executor codex \
  --model gpt-5.6-sol \
  --effort medium \
  --cwd /absolute/path/task-worktree \
  --transport cli \
  --timeout 45m
```

`--brief-file`, `--executor`, `--model` and `--cwd` are required. The model is
always explicit; `--effort` is optional only when the selected executor has no
effort selector. `--transport` accepts `cli`, `acp` or `auto` and defaults to
`cli`, preserving existing behavior.

- `cli` always selects the existing adapter command.
- `acp` requires an exact qualified executor launch and fails as unavailable
  without sending a prompt when any prerequisite is missing.
- `auto` tries ACP only when all prerequisites qualify. It may fall back to CLI
  before submission, including after a pre-prompt compatibility rejection only
  when worker shutdown was verified. It never switches transport after a
  prompt may have been submitted.

The stock command currently carries no approved qualification records, so an
explicit ACP request is unavailable and `auto` takes the CLI path. An embedding
release owner must supply the qualified lifecycle and evidence described below
before enabling an ACP launch.

The command writes one compact JSON report to stdout and puts its brief,
pre-submission intent, complete evidence and bounded stdout/stderr in a new
private artifact directory. Exit classes are `completed`, `failed`,
`waiting_input`, `rate_limited`, `unavailable`, `uncertain` and
`invalid_arguments`. `completed` still does not mean accepted.

## Receipt example

This abbreviated report demonstrates two independent kinds of provenance. The
top-level model and effort are the requested route. `usage.provenance` names the
wire field that supplied the worker counters; it does not verify provider
billing or the effective model.

```json
{
  "executor": "codex",
  "model": "gpt-5.6-sol",
  "effort": "medium",
  "requested_transport": "acp",
  "backend": "acp",
  "exit_class": "completed",
  "exit_code": 0,
  "worker_exit_code": 0,
  "receipt": {
    "submission": {"state": "submitted"},
    "transport": {"outcome": "completed"},
    "worker": {"outcome": "success"},
    "usage": {
      "cached_input_tokens": 0,
      "output_tokens": 21,
      "provenance": "acp/session-prompt/usage (draft)"
    },
    "evidence": {"path": "/private/tmp/batuta-dispatch-example/evidence.json"}
  },
  "artifacts": {
    "directory": "/private/tmp/batuta-dispatch-example",
    "brief": "brief.md",
    "intent": "intent.json",
    "receipt": "receipt.json",
    "evidence": "evidence.json",
    "stdout": "stdout.log",
    "stderr": "stderr.log"
  }
}
```

The absent `input_tokens` counter is **unknown**, not zero. The explicit zero
for cached input remains zero. Cached input is a subset of input and must never
be added to input again. A total is known only when both input and output are
reported. Context-window occupancy, byte counts and a peer-supplied total are
not substituted for missing counters. Reports are bounded to 4 KiB; overflow
remains visible and points at complete caller-owned evidence.

## Qualification and permissions

Adapter metadata only describes a possible ACP launch. It cannot qualify one.
A qualification is specific to executor ID, exact launch and version, operating
system and architecture, model, effort, permission handling, authenticated
task execution, platform coverage and verified cleanup. Evidence from one
executor, version, model or platform does not transfer to another.

The known launch families are Codex's dedicated `codex-acp` wrapper, Claude's
dedicated `claude-agent-acp` wrapper, `opencode acp`, and `cursor-agent acp`.
OpenCode and Cursor remain pilot candidates; Codex still requires permission
and cleanup qualification; Claude still requires authenticated task evidence;
Agy retains CLI. Unsupported Windows combinations remain ineligible until they
have native lifecycle evidence. No wrapper is downloaded automatically.

ACP permission requests are structured control messages. The client rejects an
unsupported request, and an unattended run cannot infer approval from prompt
text. Cancellation acknowledgement and verified worker shutdown are separate
facts; cleanup must be verified before the worktree can be discarded or an
`auto` compatibility fallback can run.

On native macOS, `acp.Process` owns a dedicated process group. Shutdown closes
its transport for cooperative EOF, then sends TERM and KILL to that group as
needed, with bounded waits. Success requires direct-child reaping and verified
group disappearance, including when the root exits before shutdown. Concurrent
and repeated shutdown calls return the same result. Discovery failures, observed
escaped survivors, signal errors and failure to drain remain unresolved.

This boundary is a managed process group, not arbitrary descendant containment.
A child that enters a new session or leaves the group can escape; advisory
process snapshots may miss a fork and reparent between observations. Discovered
PIDs are never individual signal targets. A successful group shutdown does not
qualify any provider, launch, or platform combination; qualification records and
native provider evidence are separate requirements.

## Uncertainty and rollback

The intent is durably recorded before the prompt. A disconnect, timeout,
malformed response, quota response, interruption or unverified shutdown after
possible submission is therefore `uncertain`, even if the worker later claims
success. Preserve the artifact directory and workspace and reconcile their
state against the recorded brief digest, model, effort and dispatch identity.
Do not resubmit the brief, switch to CLI or clean the worktree merely because
the ACP result is incomplete.

Rollback is prospective: choose `--transport cli` for new attempts (or omit
the flag), and remove the failing executor/version from future ACP
qualifications. An already in-flight or parked uncertain ACP attempt keeps its
reconciliation requirement. Only after an operator establishes what changed
may the normal delivery policy decide whether a subsequent CLI attempt is
safe. `auto` is not a recovery mechanism for uncertain work.

See [dispatch-measurement.md](dispatch-measurement.md) for the matched-pair
measurement protocol and [loop.md](loop.md) for loop-specific persistence and
verification behavior.
