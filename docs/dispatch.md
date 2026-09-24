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

Stock dispatch and loop task attempts use the native transport factory. It
launches a fresh managed process with fixed resolved argv, an absolute requested
workspace, inherited environment and bounded protocol I/O and owned shutdown.
Prompts travel through the protocol, never a shell command. The factory carries
three release-owned qualifications, all native **macOS arm64** (`darwin/arm64`):
OpenCode **1.18.31**, fixed launch `opencode acp`; Codex **1.13.1**, launch
`codex-acp`, session mode `read-only`; and Claude **0.81.1**, launch
`claude-agent-acp`, session mode `acceptEdits` with the sandbox
`acp_session_meta`. All three pin model `*` and empty effort, with effort
recorded per the [`not_applicable` rule](#qualification-and-permissions).
Launch matching checks only the qualification fields: `Model: *` matches any
requested model, an empty qualification effort matches any requested effort
when the adapter declares no `acp_effort_config`, and the adapter must declare
exactly the qualification's `acp_mode` and `acp_session_meta`. It does not
inspect session options.
Advertising and confirming the model and skipping the effort happen
in the session after launch (`acp.NewSession`). Executor, launch, version, platform
and the lifecycle flags still must match exactly; `*` does not transfer evidence
to another executor, version or platform. All other combinations remain unavailable
for explicit ACP; `auto` uses CLI before submission. This describes the source
constructor, not ACP availability in an installed beta23 binary.

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
task execution, platform coverage and verified cleanup. Model `*` matches any
requested model at the qualification gate; the session must still advertise
that model among its options and confirm it before the prompt. Evidence from
one executor, version or platform does not transfer to another.

The known launch families are Codex's dedicated `codex-acp` wrapper, Claude's
dedicated `claude-agent-acp` wrapper, `opencode acp`, and `cursor-agent acp`.
Three tuples have accepted native qualification evidence: the exact OpenCode
tuple above, the codex `codex-acp` tuple, and the claude `claude-agent-acp`
tuple, all on `darwin/arm64` (see the
[bridge qualification evidence](dispatch-measurement.md#codex-and-claude-bridge-qualification)).
Other OpenCode versions, efforts that require an `acp_effort_config`, and
platforms, as well as Cursor and Agy, remain on CLI. Linux and
Windows require their own native evidence; Windows also requires native
lifecycle ownership and teardown before launch. No wrapper is downloaded
automatically.

For the qualified tuple, add these existing fields to an operator-owned
`opencode` adapter's frontmatter, retaining its required CLI fields:

```yaml
acp_run: opencode acp
acp_version: 1.18.31
acp_model_config: model
```

Metadata does not grant qualification. The resolved executable must still
return exactly `1.18.31` from `opencode --version` before
every ACP launch. No install, authentication or global configuration is changed.

Beside `acp_model_config` and `acp_effort_config`, an adapter may declare two
further frontmatter fields: `acp_mode` names the session mode the factory
selects from the session's advertised modes, and `acp_session_meta` is a JSON
object sent as the `session/new` `_meta` for provider-specific options such as
sandbox settings. Neither field grants qualification.

```text
batuta dispatch \
  --brief-file /absolute/path/task.brief.md \
  --executor opencode \
  --model opencode/big-pickle \
  --cwd /absolute/path/task-worktree \
  --transport acp \
  --timeout 45m
```

The session must advertise the requested model among its options and confirm
it before receiving the prompt; a model that is missing or unconfirmed fails
explicit ACP and permits `auto` fallback only after verified pre-submission
shutdown. The receipt records `not_applicable` only when the session skipped
the effort. When the adapter declares no `acp_effort_config` and the session
advertises no thought_level option, the session records `not_applicable` after launch
instead of failing qualification. Neither path
silently changes the requested model or effort.
See the
[qualification evidence](dispatch-measurement.md#native-opencode-qualification)
for the tested scope and usage gaps.

ACP permission requests are structured control messages. The provider's own
sandbox or session mode keeps the executor inside its worktree; the stock
factory allows a permission request only when
every location it names resolves inside the worktree,
and rejects every other request. A rejection stays
terminal and non-success, even when the prompt claims approval or the worker
also reports `end_turn`. Requests that name no location are rejected too.
Unsupported client methods are rejected. Existing provider-side permissions
and configuration are inherited unchanged; the factory injects no approval,
install or auth flags. This callback policy is not an OS sandbox and does not
restrict actions the provider can already perform without requesting
permission. Adapters select the provider session mode with `acp_mode` and pass
provider options with `acp_session_meta`; neither field grants qualification.
Cancellation acknowledgement and verified worker shutdown are separate
facts; cleanup must be verified before the worktree can be discarded or an
`auto` compatibility fallback can run.

**CLI executors.** On the CLI path, containment comes from each CLI's own
sandbox and flags in its adapter, not from a batuta callback. codex
(`--sandbox workspace-write`), claude (`acceptEdits` with sandbox settings and
an in-worktree edit allow rule) and cursor-agent (`--sandbox enabled`, no
`--force`) are free inside the worktree and blocked outside it. opencode and
agy are not contained outside the worktree: opencode blocks file edits there
but writes through the shell, and agy writes both ways. They are kept by the
maintainer's decision of 2026-09-23. The
[probe evidence](../.batuta/cli-probes/2026-09-23/README.md) records each
configuration; the adapter change is batuta-ai/skills#64.

On native macOS, `acp.Process` owns a dedicated process group. Shutdown closes
its transport for cooperative EOF, then sends TERM and KILL to that group as
needed, with bounded waits. Once group disappearance is observed, no further
probe or signal targets that group ID. Already-complete direct-child reaping
succeeds immediately; pending reaping gets its own wait of at most one second,
independent of the expired stage grace period. Group absence without reaping
remains unresolved after that bound. Success requires both direct-child reaping
and verified group disappearance, including when the root exits before shutdown.
Concurrent and repeated shutdown calls return the same result. Discovery
failures remain sticky uncertainty even if later snapshots succeed: later
observations cannot reconstruct a missed interval. Discovery failures, observed
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
