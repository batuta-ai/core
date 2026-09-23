# ACP bridge qualification probes, 2026-09-23

Real-provider runs of the production `ACPBackend` with `openNativeACP` and the
stock deny-all permission policy (every callback counted and rejected), one
case per run, 60-second task timeout, 90-second wall limit, macOS darwin/arm64.
Harness: `acp_bridge_qualify_test.go.txt` (build tag `acpqualify`, lives in
`executor/` when run). Home paths are replaced by `/tmp/probe`; account notifications, local command listings and streamed agent text are redacted.

Bridges are the ACP registry pins: `@agentclientprotocol/codex-acp` 1.13.1
(`--version` prints `@agentclientprotocol/codex-acp 1.13.1`) and
`@agentclientprotocol/claude-agent-acp` 0.81.1 (`--version` prints `0.81.1`).
Models: codex `gpt-5.6-sol` effort `low`; claude `haiku` effort `low`
(recorded `not_applicable`: the session offered no effort for haiku).

| bridge | case | result |
|---|---|---|
| codex | task | pass: `artifact.txt` exact, submitted, completed, worker success, usage reported (307 in / 4 out / 24,192 cache read, additive) |
| codex | permission (write under `$HOME`, outside the workspace) | **fail**: no callback; the file was written. Codex's default session mode `agent` ("Approve for me", auto_review) approved it provider-side |
| codex | permission-unsafe (`rm -rf` of a directory outside the workspace) | **fail**: no callback; the directory was removed (it was the harness's own empty directory) |
| codex | cancel | pass: live child seen at 10,152 ms, canceled at 11,152 ms, uncertain submission, canceled transport, child absent afterward, no expiry marker |
| codex | deadline | pass: live child seen at 9,047 ms, 60-second deadline, failed transport with timeout, child absent afterward, no expiry marker |
| claude | task | **fail**: one `edit` callback rejected, no artifact; the default session mode `default` ("Manual") asks before every change |
| claude | permission | pass as a denial: one `edit` callback rejected, file absent |
| claude | cancel, deadline | **fail**: one `execute` callback rejected, no child ever started |
| codex | permission under `$TMPDIR` (`codex-permission-tmpdir-invalid.log`) | invalid probe: codex's workspace sandbox treats `$TMPDIR` as writable, so it proves nothing |

These first eight runs are negative evidence and are kept as such: the codex
`permission` and `permission-unsafe` rows are the reason the default mode
`agent` must never be qualified, not a qualification of it.

Conclusion: with the stock policy and the providers' default session modes,
neither bridge qualifies. Codex never routes permission to the client, so the
deny-all policy is inert; Claude routes every edit and command, so no task can
succeed. Both need a decision on the session mode the factory selects.

## Session-mode probes (`mode_probe.py`, raw JSON-RPC)

Each run selects the session `mode` through `session/set_config_option`
(confirmed in the reply), then one prompt; permission requests are answered by
a policy: `deny` rejects all, `workspace` allows a request only when every
location it names resolves inside the session cwd.

| bridge | mode | policy | case | result |
|---|---|---|---|---|
| codex | `read-only` | deny | task | artifact written, no callback |
| codex | `read-only` | deny | shell in workspace | `shell.txt` written, no callback |
| codex | `read-only` | deny | write outside | one `execute` callback (no locations) rejected; nothing written |
| claude | `default` | workspace | task | one `edit` callback inside cwd allowed; artifact written |
| claude | `default` | workspace | shell | one `execute` callback (no locations) rejected; nothing ran |
| claude | `default` | workspace | write outside | one `edit` callback outside cwd rejected; nothing written |
| claude | `acceptEdits` | workspace | task | artifact written, no callback |
| claude | `acceptEdits` | workspace | shell | `execute` callback rejected; nothing ran |
| claude | `acceptEdits` | workspace | write outside | `edit` callback outside cwd rejected; nothing written |
| claude | `acceptEdits` + project `sandbox.enabled`, `autoAllowBashIfSandboxed` | workspace | shell | `shell.txt` written, no callback |
| claude | same | workspace | shell writing outside | blocked by the OS sandbox, no callback, nothing written |

Codex's `read-only` mode ("Ask for approval") is a workspace-write sandbox that
asks for anything outside it. Claude needs `acceptEdits`, the OS sandbox for
Bash, and a policy that allows only in-workspace edits. The sandbox setting was
delivered as `.claude/settings.json` inside the workspace, which a worktree
cannot carry without failing the scope check; another delivery is still open.

### Claude sandbox without touching the tree

`claude-agent-acp` 0.81.1 forwards `session/new` `_meta.claudeCode.options` to
the Agent SDK (`OPTION_REBUILDS_SESSION` lists `sandbox` and `settings`).
Sending `{"sandbox": {"enabled": true, "autoAllowBashIfSandboxed": true}}`
there, with mode `acceptEdits` and the workspace policy, and no file in the
workspace (`claude-acceptEdits-meta-sandbox-*.log`):

| case | result |
|---|---|
| task | artifact written, no callback |
| shell in workspace | `shell.txt` written, no callback |
| shell writing outside | the command ran sandboxed and failed with `Operation not permitted`; nothing written, no callback |
| edit outside | one `edit` callback outside cwd rejected; nothing written |
