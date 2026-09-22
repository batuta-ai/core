# Plan — judge polish: defaults, JSON output, timeouts, cost in replay
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Fix the four rough edges found while probing the real providers on 2026-09-20: a single provider demands `model` although each provider has a documented default; `batuta judge ask|probe` print Go field names because `Response` has no JSON tags; the 3 s default timeout is too short for OpenRouter and TypeSafe on a cold connection; and replay prints no token usage, so the benchmark cannot state cost.
**Created:** 2026-09-21 · **Status:** done

## Tasks
- [x] 1. Provider model defaults, JSON tags and a 10 s timeout — backend/medium
      Scope: judge/judge.go, judge/config.go, judge/config_test.go, judge/http.go, judge/http_test.go, cmd/batuta/judge_test.go, docs/judge.md
      Accept: a single-provider config without model loads with the provider's default (jev-latest for typesafe, typesafe-ai/jev for vercel, typesafe/jev-1.13 for openrouter) and an explicit model still wins → go test ./judge -run TestLoadConfigModelDefaults; Response, Answer and Usage marshal with snake_case keys model, answers, usage, input_tokens, output_tokens, type, noul, choice, score, confidence, probabilities and omit zero-valued optional fields → go test ./judge -run TestResponseJSON; the default timeout is 10000 ms and the accepted range stays 500 ms to 30 s → go test ./judge -run 'TestLoadConfigDefaults|TestLoadConfigRejects'; batuta judge ask prints the snake_case shape → go test ./cmd/batuta -run TestJudgeAskCommand; docs/judge.md states the defaults per provider and the new timeout → grep -q '10000' docs/judge.md; the package and command tests stay green → go test ./judge ./cmd/batuta
- [x] 2. Usage, latency and totals in probe and replay — backend/medium
      Depends on: 1
      Scope: cmd/batuta/judge.go, cmd/batuta/judge_test.go, docs/judge.md
      Accept: batuta judge probe prints one line with provider, model, latency in milliseconds and input and output tokens → go test ./cmd/batuta -run TestJudgeProbeCommand; each replay line ends with tokens=<input>/<output> ms=<latency> and the command ends with a totals line attempts=<n> asked=<n> skipped=<n> input_tokens=<sum> output_tokens=<sum> → go test ./cmd/batuta -run TestJudgeReplayCommand; replay accepts --json and then prints one JSON object per attempt plus a final totals object instead of the text lines → go test ./cmd/batuta -run TestJudgeReplayJSON; docs/judge.md documents the totals line and --json → grep -q 'input_tokens' docs/judge.md; the module builds and the suite passes → go build ./... && go test ./...

## Decisions and context

Go standard library only, frozen exported signatures except additions in `judge` and `cmd/batuta`, conventional commits, tests against httptest and fake judges only, no secrets. Observed on 2026-09-20 with real keys: `batuta judge probe` against TypeSafe took 8.1 s on the first call and 0.79–1.57 s afterwards; OpenRouter answered in between 3 and 15 s; both hit the 3 s default. `curl` to the same endpoint answered in 0.66 s, so the slow first call is connection setup, not the model.

**Task 1.** In `judge/config.go`, `applyDefaults` sets `Model` per provider when empty and `validate` stops requiring it for single providers (it stays rejected together with `auto`, unchanged). Add `json` tags to `Response`, `Answer` and `Usage` in `judge/judge.go` matching the TypeSafe wire names so the CLI output equals the API shape; `Answer` fields `noul`, `choice`, `score`, `confidence`, `probabilities` use `omitempty` and `Answer.Noul` keeps its numeric zero meaningful for noul answers (test that a noul of 0 still marshals; use a pointer or a custom marshal only if needed and keep the exported field types). `defaultTimeoutMS` becomes 10_000. Update the config table and the providers table in `docs/judge.md`.

**Task 2.** `judge probe` success line: `provider=<p> model=<m> ms=<latency> tokens=<in>/<out>`. Replay: extend the per-attempt line and add the totals line; `--json` emits `{"task_id","execution","outcome","answers":{...},"provider","model","input_tokens","output_tokens","latency_ms"}` per attempt and `{"totals":{...}}` last. Latency is measured around `Ask` with the runner's clock. Keep the existing text format otherwise byte-identical so the benchmark file's earlier lines remain comparable.
