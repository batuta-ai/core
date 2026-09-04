# batuta-ai/core

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

No package imports a daemon SDK. Everything runs over `git`, `gh` and the
executor CLIs through `publication.CommandRunner`.

## Roadmap

- `journal`, `worktree` and `executor` interfaces so the same graph runs with a file journal, `git worktree` and subprocess executors on any CLI host, and with the CompozyOS daemon in the extension.
- `gates`: the four mechanical verification gates.
- `cmd/batuta`: `doctor`, `inventory`, `gate`, `trail`, `loop`.

## Develop

```bash
go build ./... && go vet ./... && go test ./...
```

Tests use a canonical temporary directory (`tempDir(t)`): on macOS the
default `t.TempDir()` sits under a symlinked `/var`, and the trusted-root
checks compare paths after `filepath.EvalSymlinks`.

## License

[MIT](LICENSE)
