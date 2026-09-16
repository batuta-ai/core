<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/batuta-ai/.github/main/brand/readme-header-core-dark.png">
  <img src="https://raw.githubusercontent.com/batuta-ai/.github/main/brand/readme-header-core-light.png" width="100%" alt="batuta-ai / core — Núcleo em Go: roteamento e coordenação de agentes. Go core: agent routing and coordination.">
</picture>

> *Quem rege não toca.* — The conductor does not play.

The Go core of Batuta: the deterministic parts of the conducting cycle that
no host should reimplement in prose. Extracted from
[batuta-ai/compozy](https://github.com/batuta-ai/compozy) with its history;
that extension now depends on this module, and the file hosts in
[batuta-ai/batuta](https://github.com/batuta-ai/batuta) ship the `batuta`
binary built here. The doctrine and skills live in
[batuta-ai/skills](https://github.com/batuta-ai/skills).

## Versioning

The module is **pre-release**: `v1.1.0-beta.N` until the API stabilizes.
`v1.0.0` and `v1.0.1` are retracted (published before the beta line);
`go get github.com/batuta-ai/core@latest` resolves to the current beta.

## Packages

| Package | Owns |
|---|---|
| `routing` | the delivery graph (dependency-safe waves of at most four tasks, candidates, canonical integration, conflict re-execution, pauses, budgets), the immutable routing generation, domain × complexity selection, task ownership and classification, task artifacts |
| `inventory` · `inventory/adapters` | redacted executor inventory: probes for `codex`, `opencode`, `cursor-agent`, `claude`, `agy`; resolution states `resolved / declared / unknown` |
| `integration` | one task = one commit: candidate evidence in a task worktree, verified integration into the canonical worktree, tracking digests |
| `publication` | command runner with output limits, git snapshots and ancestry, publication plan and independent verification of the reviewed HEAD |
| `repository` | guarded repository bootstrap: `.gitignore`-aware, blocks unignored sensitive paths, one `chore: initialize workspace` commit |
| `journal` | append-only, hash-chained JSONL per delivery under `.batuta/journal/`; every record carries the graph after the transition, so `--resume` continues from the last one |
| `worktree` | `git worktree` per task attempt under `.batuta/worktrees/`, squash to one commit, bookkeeping commits, `.git/info/exclude` |
| `executor` | adapter frontmatter to argv, legacy CLI execution, and opt-in qualified ACP sessions with compact receipts and verified shutdown — see [docs/dispatch.md](docs/dispatch.md) |
| `gates` | the four mechanical gates: finished · tree · tests · verify (scope, proofs, independent read-only verifier) |
| `loop` | `batuta loop`: the mechanical conductor over `routing.DeliveryGraph` on file hosts — see [docs/loop.md](docs/loop.md) |
| `review` | `batuta review`: cohort-based, read-only delivery review with a mechanical verdict — see [docs/review.md](docs/review.md) |

No package imports a daemon SDK. Native host children remain host-owned. Core
runs external executors through the legacy CLI or its bounded ACP client.

## The binary

`batuta version` · `capabilities` · `inventory` · `doctor` · `dispatch` · `loop` · `trail`.
Skills probe `batuta capabilities` before calling a subcommand.

```
batuta loop --dry-run [<plan>]          waves, executors, worktrees; runs nothing
batuta dispatch --brief-file <path> --executor <id> --model <id> --cwd <worktree>
                                        one bounded external attempt; CLI by default
batuta loop --roadmap [--dry-run]       run the approved roadmap delivery
batuta loop [<plan>]                    run the approved plan to a terminal state
batuta loop --resume <delivery>         continue after an interruption
batuta loop --answer <task> "<text>"    answer a parked task and continue
batuta loop --abandon <delivery>        close a delivery; ticks what integrated
batuta loop --supervise <delivery> --cursor <absolute-path>
                                        foreground local observation; no model polling
batuta loop --dashboard [<delivery>]    one TSV snapshot of delivery state
batuta review --base <ref> [--spec <plan>] review a delivery through adapters
batuta watch [<delivery>]               live panel dashboard (watch by default)
batuta trail [<delivery>]               one line per journal record
```

Both `dispatch` and `loop` accept `--transport cli|acp|auto`; omission keeps
the legacy CLI path. ACP requires exact per-executor/version/platform/model
qualification. The stock command has no approved ACP launches. Uncertain ACP
work is preserved for reconciliation and never replayed through CLI
automatically. Native subagents are selected by interactive hosts, not by this
binary. See [dispatch](docs/dispatch.md) and the
[measurement protocol](docs/dispatch-measurement.md).

Foreground [loop supervision](docs/loop-supervision.md) is enabled by default
for new, resume, answer and roadmap execution, with a durable cursor at
`.batuta/runs/supervision/<delivery>.json`. Worker logs remain on stdout and
observer JSON goes to stderr. Dry-run, dashboard and abandon launch no reviewer.
`--supervise` remains available as a separate attachment with JSON stdout. It requires a process kept alive by the host;
local file and supported desktop notifications are opt-in, and no sink leaves
events durably unread. Observation makes zero model calls. After completion,
the supervisor runs a full review of the immutable delivery even without
`--policy`. A pending review returns `review_blocked` (exit `2`); runtime or
evidence errors exit `1`. Missing, failed, incomplete or uncertain review
durably blocks subsequent roadmap phases. Complete `SHIP` clears progression
only. Resume with `batuta loop --resume <delivery>` or rerun `--roadmap`; review
budgets and uncertain attempts are preserved. Inspect without execution using
`batuta loop --supervise <delivery> --review-status`. Explicit judgment uses
`--review-judgment accept|reject --review-id <id> --review-digest <sha256:digest>
--rationale "<reason>"` with the same delivery; see the supervision guide for
exact usage and recovery limits. A policy can provide
the fixed scoped worker answer and resume its assigned task with normal routed
execution settings, or reserve an explicitly authorized correction proposal.
A resumed completion enters the same automatic review; correction proposals
never start a runner. Implementation completion and review outcome remain separate from
conductor acceptance. `job.json`, the immutable spec copy, and the source
snapshot are under `.batuta/reviews/supervision/<job-id>/`; only engine output
is in its `artifacts/` subdirectory.
The supervision CLI has no review-timeout flag, so its review timeout is fixed
at the one-hour default. Cancellation or timeout while waiting for review
ownership returns an error without a job transition. An interrupted probe or
snapshot is `failed`/`execution_failed`. Cancellation or timeout while the
engine runs is `uncertain`/`cleanup_unresolved` and is not replayed
automatically. Post-exit verification can instead be `failed`/`execution_failed`.
Recovery from a durable `launching` state is `uncertain`; its outcome may remain
unset, and it is not replayed automatically.

When a usage limit outlasts the wait budget, the loop falls back to the next
executable runtime without spending a retry or escalation.

The dashboard groups tasks by wave and shows execution context, progress
bars, selected-task detail, attempts, gates G0–G3, commits, and the active
executor log. It shows loop presence, opens a multi-line answer editor with
`r`, resumes a submitted answer detached, opens the delivery picker with `d`,
and never exits on its own. Use `?` for the full key and presence legend,
`--once` for a single non-interactive snapshot, or `--interval` to change the
live refresh period. Without a TTY, keyboard input is disabled and the active
task is followed automatically; `NO_COLOR` and non-UTF-8 locale fallbacks keep
redirected output readable.

## Roadmap

- `batuta loop --roadmap [--dry-run]`: the roadmap delivery runner.
- `cmd/batuta gate <name>`: the gates as standalone subcommands for the interactive skill.
- Delivery-level token budgeting; optional ACP receipt counters are retained
  for the external matched-pair measurement protocol.

## Develop

```bash
go build ./... && go vet ./... && go test ./...
```

Tests use a canonical temporary directory (`tempDir(t)`): on macOS the
default `t.TempDir()` sits under a symlinked `/var`, and the trusted-root
checks compare paths after `filepath.EvalSymlinks`.

## License

[MIT](LICENSE)
