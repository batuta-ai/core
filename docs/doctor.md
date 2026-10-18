# batuta doctor

`batuta doctor [--workspace <dir>] [--json] [--timeout <duration>]` reports what a conductor needs to know before the first cycle:

- which executors are installed and usable;
- whether the workspace is a git repository, and whether its tree is clean;
- whether the Batuta skills are installed;
- which tools would scan `.batuta/` (see [Tool ignores](#tool-ignores)).

The output is human text, or JSON with `--json`. `doctor` reports only. It never edits a file; `/batuta-init` does the patching.

## Tool ignores

Orchestrator state lives in `.batuta/`: run trails, review snapshots and journals stay there whatever `Worktree-home` says. A test runner, linter, formatter or type checker that scans it can fail the project's gate on orchestrator files. When the workspace root has a `.batuta/` directory, `doctor` checks each tool it finds and says whether the tool keeps out of it. The check does not read the profile, and it does nothing without a `.batuta/` directory.

The tools are `vitest`, `jest`, `playwright`, `eslint`, `prettier`, `biome`, `typescript` and `pytest`. `doctor` finds one when the workspace root configures it or the root `package.json` scripts run it.

Each finding has one status. The text and the JSON use the same strings:

| Status | Meaning |
|---|---|
| `ignored` | The tool already keeps out of `.batuta/`. |
| `not_needed` | The tool skips `.batuta/` on its own, or its settings never select it. |
| `missing` | The tool would scan `.batuta/`. The hint gives the exact entry to add. |
| `unknown` | `doctor` cannot tell. The hint says what to check, or gives the read, parse or lex error. |

No status changes the exit code of `doctor`.

In text, each finding is one line after the `skills` line:

```
tools       vitest vitest.config.ts missing — add '**/.batuta/**' to test.exclude, ...
```

The line holds the tool, the config file, the `package.json` key in parentheses when the config came from one, and the status. A `missing` or `unknown` finding adds ` — ` and the hint. If the check itself fails, the line is `tools       check failed: <error>` and `doctor` still prints the rest of the report.

In JSON, `tool_ignores` is always an array, empty when there is nothing to report. Each entry has `tool`, `config`, `status`, and when they apply `key` and `hint`. `tool_ignores_error` appears only when the check failed.

## Limits

The report is advice. The project's own gate is the proof.

- Configs are read, never run. A `lexer` strips comments and reads string literals, object keys and call names of JavaScript and TypeScript configs. A comment that mentions `.batuta` proves nothing. A source the lexer misreads is reported as `unknown`, never as `ignored`.
- An ignore list that comes from a shared preset is invisible to `doctor`. It does not check which object an ignore key belongs to, beyond the ESLint rule that a global `ignores` stands alone in its object.
- A package extends in `tsconfig.json` is not read. Only a relative `extends` is followed. `tsc` wildcards skip dot-directories, so a base config could reach `.batuta` only by naming it.
- Only the root `tsconfig.json` is checked: `tsconfig.*.json` variants and `tsc -p <file>` in scripts are not.
- Only files at the workspace root are read, plus a relative TypeScript `extends` inside the root. A config inside a monorepo workspace package does not walk the root and needs nothing.
- `.gitignore` counts for Prettier only.
