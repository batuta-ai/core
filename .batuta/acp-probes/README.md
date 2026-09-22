# ACP and CLI probes, 2026-09-22

Raw JSON-RPC and CLI output recorded by the conductor while deciding which
transport each executor should use. Home paths are replaced by `/tmp/probe`.

| file | what it shows |
|---|---|
| `opencode-big-pickle.log`, `opencode-glm-5.3-flash.log` | `opencode acp` accepts either model; the session confirms it in `config_option_update`; `session/prompt` returns `usage` (input, output, total, cachedRead, thought) and `session/update` sends `usage_update` with `cost {amount, currency}` |
| `codex-acp-gpt-5.6-sol.log` | `@agentclientprotocol/codex-acp` 1.12.0 offers gpt-6-astra, gpt-5.6-sol/terra/luna and effort low…ultra, and reports usage; the stale `@zed-industries/codex-acp` 0.16.0 stops at gpt-5.5 and returns errors without an id |
| `claude-agent-acp.log` | `@agentclientprotocol/claude-agent-acp` reports input, output, cachedRead, cachedWrite, total and cost |
| `cursor-agent-grok.log` | `cursor-agent acp` works and switches to `grok-4.6[effort=high,fast=true]`, but sends no usage at all |
| `antigravity-acp.log` | the official `agy_acp_server.par` 1.1.1 needs its own Google login, lists the gemini-3.8/3.7/3.6 flash models, and sends no usage |
| `false-positive-limit-2026-09-22.out.log` | the executor report whose words ("…is rate limited…") the opencode `limit_regex` mistook for a provider limit; the first real hard negative for that decision (fixed in skills#60) |

The CLI stream fixtures live in `executor/testdata/stream/`.
