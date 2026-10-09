# Plan — batuta council: fixes from the branch review
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the accepted findings of `batuta review --base main --full` over branch `feat/council` (2026-10-08): the critique prompt carries the profile's own Conventions, an empty chairman answer is a failure, the JSON secret-field rule stops matching benign keys such as `input_tokens`, and an incomplete council records INCOMPLETE in its artefacts.
**Created:** 2026-10-08 · **Status:** done

## Tasks
- [x] 1. the critique prompt carries the profile's Conventions — backend/medium
      Scope: loop/profile.go, loop/profile_test.go, cmd/batuta/council.go, cmd/batuta/council_test.go
      Accept: a helper in `loop/profile.go` returns the body of the `## Conventions` section of `.batuta/profile.md` (empty when absent) → go test ./loop -run TestProfileConventionsSection; `batuta council` passes that section first, then the template chain's Conventions sections, so a rule that exists only in the profile appears in every critique prompt → go test ./cmd/batuta -run TestCouncilPromptHasProfileConventions
- [x] 2. an empty chairman answer is a council failure — backend/medium
      Depends on: 1
      Scope: council/run.go, council/run_test.go
      Accept: a chairman session that exits 0 with only blank output records a council failure (label `chairman`, stage `chairman`, reason naming the empty answer) and leaves the synthesis empty instead of accepting it → go test ./council -run TestRunEmptyChairman; a chairman answer with text is still the synthesis → go test ./council -run TestRunStages
- [x] 3. the JSON secret-field rule matches secret-bearing key names only — backend/medium
      Scope: executor/tail.go, executor/tail_test.go
      Accept: lines whose JSON keys are `token`, `access_token`, `refresh_token`, `api_key`, `apikey`, `password`, `secret`, `client_secret`, `access_key` or `private_key` (case-insensitive, as the whole key or after `_` or `-`) are dropped → go test ./executor -run TestDropSecretFieldLinesNames; lines whose keys are `input_tokens`, `output_tokens`, `token_count`, `tokenizer`, `max_tokens`, `secretary` or `passwordless_login` are kept → go test ./executor -run TestDropSecretFieldLinesBenignKeys
- [x] 4. an incomplete council records INCOMPLETE in its artefacts — backend/medium
      Depends on: 2
      Scope: cmd/batuta/council.go, cmd/batuta/council_test.go
      Accept: when fewer than two critiques parse, `council.json` has recommendation `INCOMPLETE` and `council.md` says INCOMPLETE, and the command exits 4 → go test ./cmd/batuta -run TestCouncilIncompleteArtefacts

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`, temp dirs through `tempDir(t)`. `cmd/batuta` must build on linux, darwin and windows. Release tooling is never touched. Tests use fixtures and fake runners; no test calls a real model.

Environment setup is never a question. `GOCACHE` is set to a writable directory and the Go 1.26.4 toolchain is first on PATH with `GOTOOLCHAIN=local`: run `go test` plainly. If your shell still has `GOROOT` pointing to the mise Go 1.24.2 install, run `env -u GOROOT go test …`; if that is refused too, finish the code and tests, say so in your report and stop — the conductor's gate runs the suite.

Declined in the branch review and not to implement: rejecting prose between `COUNCIL>>>` and the verdict. The council-fixes-3 criterion was corrected by the conductor's journalled answer (option a); prose there keeps parsing.

**Task 1.** `cmd/batuta/council.go` (~90–103) loads the profile with `loop.LoadProfile` and passes only `loop.Conventions(skills, profile.Template)`, the template chain's "Conventions for briefs" sections. `Profile.Raw` holds the whole file; `sectionOf` in `loop/profile.go` (~194) extracts a section. Do not change what loop briefs carry; that is a separate decision.

**Task 2.** `council.Run` (`council/run.go` ~165–178) accepts `final[0].output` as the synthesis whenever the chairman session has no failure.

**Task 3.** `secretField` (`executor/tail.go` ~12) matches the four words anywhere inside a JSON key, so every stream-json usage event (`input_tokens`) is dropped from tails. `DropSecretBearingLines` and `secretAssign` stay as they are.

**Task 4.** `cmd/batuta/council.go` (~107–120) serializes `result.Aggregate` even when `council.Run` returned an error after the sessions started, so the zero-value recommendation reaches `council.json`.
