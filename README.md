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
| `executor` | adapter frontmatter (`skills/batuta/adapters/*.md`) to argv — never a shell — subprocess with stdin closed, timeout and process-group kill, `finished` and `limit_regex` rules |
| `gates` | the four mechanical gates: finished · tree · tests · verify (scope, proofs, independent read-only verifier) |
| `loop` | `batuta loop`: the mechanical conductor over `routing.DeliveryGraph` on file hosts — see [docs/loop.md](docs/loop.md) |

No package imports a daemon SDK. Everything runs over `git`, `gh` and the
executor CLIs through `publication.CommandRunner`.

## The binary

`batuta version` · `capabilities` · `inventory` · `doctor` · `loop` · `trail`.
Skills probe `batuta capabilities` before calling a subcommand.

```
batuta loop --dry-run [<plan>]          waves, executors, worktrees; runs nothing
batuta loop [<plan>]                    run the approved plan to a terminal state
batuta loop --resume <delivery>         continue after an interruption
batuta loop --answer <task> "<text>"    answer a parked task and continue
batuta loop --abandon <delivery>        close a delivery; ticks what integrated
batuta loop --dashboard [<delivery>]    TSV state of the open deliveries
batuta trail [<delivery>]               one line per journal record
```

## Roadmap

- `cmd/batuta gate <name>`: the gates as standalone subcommands for the interactive skill.
- Token accounting for CLI executors that report it.

## Develop

```bash
go build ./... && go vet ./... && go test ./...
```

Tests use a canonical temporary directory (`tempDir(t)`): on macOS the
default `t.TempDir()` sits under a symlinked `/var`, and the trusted-root
checks compare paths after `filepath.EvalSymlinks`.

## License

[MIT](LICENSE)
