# Routing — core

<!-- inputs: profile.md@sha256:e18a00765937 -->

Confirmed with the user by /batuta-init on 2026-09-06; research ladder reseated on 2026-09-19; codex removed from every row on 2026-09-20 because the ChatGPT usage limit is too low. Installed and probed: agy, claude, cursor-agent, opencode. cursor-agent runs Grok only (no Claude models there, by the user's choice). Model IDs come from `batuta inventory` and each adapter's `models` line on this machine.

Reviewed on 2026-09-22 and kept as the provisional default (agy low, opencode medium, cursor-agent high, self critical): an independent read-only review over 59 journals found them working defaults, not proven optima, with worker cost unmeasured and no implementation runs for any other inventory model (`.batuta/judge-review-astra-lanes.md`). Change a row only on recorded outcome evidence, not on model names.

| Lane | Domain | Executor | Model | Cost |
|---|---|---|---|---|
| low | * | agy | gemini-3.8-flash-low | free quota |
| medium | * | opencode | opencode/glm-5.3-flash | opencode credits, cents |
| high | * | cursor-agent | cursor-grok-4.6-high | Cursor subscription, Grok 4.6 high |
| critical | * | self | — | host |

| Role | Lane | Executor | Model | Cost |
|---|---|---|---|---|
| research | low | agy | gemini-3.8-flash-low | free quota |
| research | medium | opencode | opencode/glm-5.3-flash | opencode credits, cents |
| research | high | cursor-agent | cursor-grok-4.6-high | Cursor subscription, native read-only `--mode ask` |
