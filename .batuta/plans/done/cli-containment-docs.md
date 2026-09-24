# Plan — the permission docs state how each CLI executor is contained
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** `docs/dispatch.md` states the worktree model for ACP only; add how the CLI executors are contained, from the probes in `.batuta/cli-probes/2026-09-23/README.md` and the adapter change in batuta-ai/skills#64.
**Created:** 2026-09-23 · **Status:** done

## Tasks
- [x] 1. docs/dispatch.md gains a CLI containment paragraph — docs/medium
      Scope: docs/dispatch.md
      Accept: right after the ACP worktree-permission paragraph, a new paragraph titled "CLI executors" states that containment on the CLI path comes from each CLI's own sandbox and flags in its adapter, that codex (`--sandbox workspace-write`), claude (`acceptEdits` with sandbox settings and an in-worktree edit allow rule) and cursor-agent (`--sandbox enabled`, no `--force`) are free inside the worktree and blocked outside, and that opencode and agy are not contained outside the worktree and are kept by the maintainer's decision of 2026-09-23 → grep -q 'CLI executors' docs/dispatch.md && grep -q 'not contained outside the worktree' docs/dispatch.md; it links the probe evidence → grep -q 'cli-probes/2026-09-23' docs/dispatch.md; the package stays green → go test ./executor

## Decisions and context

Docs only, conventional commits. The ACP paragraph starts near docs/dispatch.md line 191 ("sandbox or session mode keeps the executor inside its worktree"); do not change it. Facts to copy, and nothing more: the table in `.batuta/cli-probes/2026-09-23/README.md`.
