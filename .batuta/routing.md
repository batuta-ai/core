# Routing — core

<!-- inputs: profile.md@sha256:e18a00765937 -->

Confirmed with the user by /batuta-init on 2026-09-06; research ladder reseated on 2026-09-19; codex removed from every row on 2026-09-20 because the ChatGPT usage limit is too low. Installed and probed: agy, claude, cursor-agent, opencode. cursor-agent runs Grok only (no Claude models there, by the user's choice). Model IDs come from `batuta inventory` and each adapter's `models` line on this machine. Reseated on 2026-09-24: opencode Zen credits ran out (`Insufficient account funds`) and the cursor-agent executable is missing, so `medium` moved to claude sonnet and `high` to codex gpt-6-sol (the account is ChatGPT Pro); agy stays on `low` by the user's choice.

Reviewed on 2026-09-22 and kept as the provisional default (agy low, opencode medium, cursor-agent high, self critical): an independent read-only review over 59 journals found them working defaults, not proven optima, with worker cost unmeasured and no implementation runs for any other inventory model (`.batuta/judge-review-astra-lanes.md`). Change a row only on recorded outcome evidence, not on model names.

| Lane | Domain | Executor | Model | Cost |
|---|---|---|---|---|
| low | * | agy | gemini-3.8-flash-low | free quota |
| medium | * | claude | sonnet | Claude subscription, CLI contained by sandbox settings |
| high | * | codex | gpt-6-sol | ChatGPT Pro, `--sandbox workspace-write` |
| critical | * | self | — | host |

| Role | Lane | Executor | Model | Cost |
|---|---|---|---|---|
| research | low | agy | gemini-3.8-flash-low | free quota |
| research | medium | claude | sonnet | Claude subscription, read-only (`--disallowedTools`) |
| research | high | codex | gpt-6-sol | ChatGPT Pro, native `--sandbox read-only` |
