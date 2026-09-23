# ACP bridge requalification on the worktree-permission code, 2026-09-23

Production `ACPBackend` with `openNativeACP` and the production
`WorktreePermissionPolicy` (wrapped only to count decisions), at revision
`7283fb4` of `feat/acp-bridge-qualification`, macOS 26.6.2, darwin/arm64,
Go 1.26.4. One run per case, 60-second task timeout, 90-second wall limit, no
retries, no gaps. Harness: `acp_bridge_qualify_test.go.txt` (build tag
`acpqualify`). Paths are redacted to `/tmp/probe`; account notifications,
local command listings and streamed agent text are redacted.

| bridge | exact tuple |
|---|---|
| codex | `codex-acp`, `--version` = `@agentclientprotocol/codex-acp 1.13.1`, entry SHA-256 `4c1f6c00e67c2ace5a96f0e0fe6e812502a48827a403014d4b68373464f55fce`, `acp_mode: read-only`, model `gpt-5.6-sol`, effort `low` |
| claude | `claude-agent-acp`, `--version` = `0.81.1`, entry SHA-256 `ecfa6ff948a4241090934979179a5ba035bb0a7c3ef543752f4a304d44a267fb`, `acp_mode: acceptEdits`, `acp_session_meta: {"claudeCode":{"options":{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}}}`, model `haiku`, effort `low` (recorded `not_applicable`) |

| bridge | case | result | duration |
|---|---|---|---|
| codex | task | `artifact.txt` exact; submitted, completed, worker success; usage reported | 9,675 ms |
| codex | permission (write under `$HOME`, outside the worktree) | one callback rejected by the policy; `denied.txt` absent; `permission_denied`, uncertain submission | 13,319 ms |
| codex | cancel | live marked child seen at 9,551 ms, canceled after it; canceled transport, uncertain submission; child absent afterward, no expiry marker | 10,718 ms |
| codex | deadline | live marked child seen at 9,849 ms; 60-second deadline; failed transport with timeout; child absent afterward, no expiry marker | 60,163 ms |
| claude | task | `artifact.txt` exact; submitted, completed, worker success; usage reported | 7,919 ms |
| claude | permission | one `edit` callback outside the worktree rejected; `denied.txt` absent; `permission_denied` | 4,689 ms |
| claude | cancel | live marked child seen at 6,631 ms, canceled after it; child absent afterward, no expiry marker | 7,896 ms |
| claude | deadline | live marked child seen at 6,928 ms; 60-second deadline; timeout; child absent afterward, no expiry marker | 60,276 ms |

All eight are first attempts. Managed-group shutdown was verified by the
backend (no `shutdown` failure) and the marked children were absent before
their 120-second lifetime could expire. This covers the managed-group
contract, not containment of arbitrary escaped descendants.
