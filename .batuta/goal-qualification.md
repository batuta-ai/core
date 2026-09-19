# Goal qualification — 2026-09-13

User requested evaluating whether each CLI's goal can sustain Batuta supervision and whether their semantics are equivalent. This is a qualification follow-up, not an implementation approval or a change to active delivery graphs.

## Initial evidence

- Local Codex 0.154.0: `codex features list` reports `goals stable true`. End-to-end supervision not qualified.
- codex-acp upstream documents an experimental negotiated extension: initialize `_meta.goal`, supported actions, `_session/goal`, updates in `session_info_update._meta.goal`. Goal lifetime differs from prompt lifetime. Source: https://github.com/agentclientprotocol/codex-acp/blob/main/docs/goal-extension.md
- `codex-acp` is not on this shell's PATH; this does not establish absence of another installed adapter path.
- OpenCode documents ACP and bounded agent steps; persistent autonomous goals equivalent to Codex are not established. Source: https://opencode.ai/docs/agents/
- Local OpenCode, Claude, Cursor agent and agy expose session continuation in help. Continuation is not proof of autonomous goals. Their goal capabilities remain unqualified; absence from top-level help does not prove absence of slash commands or extensions.

## Required evidence per CLI and transport/version

1. Discover supported controls without assuming feature parity or injecting a slash command as an API.
2. Demonstrate autonomous continuation after a completed turn, without user status prompts.
3. Distinguish executing, waiting for events, blocked, paused, budget exhausted and complete; verify event delivery without repeated model polling.
4. Interrupt the process and test persistence, explicit recovery and duplicate-execution prevention. Distinguish restart recovery from independent process resurrection.
5. Exercise pause/cancel and confirm all owned work has stopped before immutable snapshot review.
6. Measure tokens, elapsed time and available cost for orchestration plus executors plus review; unavailable usage stays unknown. Compare with existing event-driven supervisor, not an artificial busy-poll baseline.
7. Confirm goal completion cannot bypass Batuta gates, deep review, scope, authorization or aggregate retry/budget limits.
8. Assign one continuation owner per session; avoid supervisor retries competing with native goal continuations.

Use a disposable fixture and bounded runs. Record supported, partial, unsupported or unverified with evidence. No live delivery experiment, installation or global configuration change during discovery.

## Architecture hypothesis to validate

A CLI goal may supply the conductor's continuation lifecycle and reduce the custom supervisor needed. Batuta retains durable delivery identity, ownership, deterministic gates, review evidence and reconciliation. A minimal external watchdog is needed only for guarantees not supplied by the hosting runtime, such as recovery after the goal host itself dies. Instructions express policy; demonstrated runtime behavior establishes reliability. Prefer one active supervisor and event-driven waiting, with model calls only for actionable decisions.
