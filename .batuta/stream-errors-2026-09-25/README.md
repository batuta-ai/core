# Real provider error outputs in each CLI's JSON mode, 2026-09-25

Captured by the conductor with each CLI's stream/JSON flag. Local hook
events, the `init` event (tools, skills, paths) and one codex skills notice
were removed; home paths are `/tmp/probe`.

| file | command | what it shows |
|---|---|---|
| `claude-invalid-model.jsonl` | `claude -p --output-format stream-json --verbose --model invalid-model-x` | synthetic assistant text plus `result` with `is_error: true`, `api_error_status: 404`, `terminal_reason: api_error` |
| `claude-ok-haiku.jsonl` | same, `--model haiku` | a normal run carries `rate_limit_event` with `rate_limit_info.status: allowed`, `rateLimitType`, `resetsAt` |
| `codex-invalid-model.jsonl` | `codex exec --json -m invalid-model-x` | `item.completed` with `item.type: error`, then `error` and `turn.failed` carrying the provider's 400 body |
| `opencode-zen-glm.jsonl` | `opencode run --format json --model opencode/glm-5.3-flash` | `error` with `APIError`, `statusCode: 402`, `Upstream request failed: Insufficient account funds` (the Zen account was out of credit) |
| `opencode-invalid-model.jsonl` | `opencode run --format json --model deepseek/invalid-model-x` | `error` with `UnknownError` |
| `agy-invalid-model.jsonl` | `agy -p … --output-format stream-json --model invalid-model-x` | `{"event":"result","result":{"status":"ERROR","error":"invalid model selection …"}}` |

The matching `.stderr` files hold what each CLI wrote to stderr.
