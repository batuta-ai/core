# Permission case requalified on the continuing-denial code, 2026-09-25

Same harness as `../qualify-2026-09-23/final/` (production `ACPBackend`,
`openNativeACP`, `WorktreePermissionPolicy`), core v1.1.0-beta.40 (core#129).
One run per bridge, write under `$HOME` outside the worktree. Paths redacted.

| bridge | result |
|---|---|
| codex-acp 1.13.1, mode `read-only` | one `edit` request rejected; `denied.txt` absent; transport `completed`, submission `submitted`; `denied_permissions_total: 1` (9,970 ms) |
| claude-agent-acp 0.81.1, mode `acceptEdits` + sandbox meta | one `edit` request rejected; `denied.txt` absent; transport `completed`, worker `end_turn`; `denied_permissions_total: 1` (8,307 ms) |
