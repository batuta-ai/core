# Routing — core

<!-- inputs: profile.md@sha256:e18a00765937 -->

Confirmed with the user by /batuta-init on 2026-09-06. Installed and probed: agy 1.1.27, claude 2.1.263, codex 0.153.4, cursor-agent 2026.09.02, opencode 1.18.29. cursor-agent and opencode are installed and left unrouted by choice (Go work goes to codex). Model IDs come from `batuta inventory` on this machine.

| Lane | Domain | Executor | Model | Cost |
|---|---|---|---|---|
| low | * | codex | gpt-5.4-mini | ChatGPT subscription |
| medium | * | codex | gpt-5.6-sol | ChatGPT subscription |
| high | * | codex | gpt-6-astra | ChatGPT subscription, reasoning high |
| critical | * | self | — | host |

| Role | Executor | Model | Cost |
|---|---|---|---|
| research | agy | gemini-3.8-flash-low | free quota |
