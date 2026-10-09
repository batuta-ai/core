# Plan — batuta council: fixes from the delivery review
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the seven findings of the final review of delivery `council-20261008-125821`: a council answer is parsed even when an output decoder dropped its JSON finding lines, failure tails never keep JSON secret fields, artefacts are written atomically, and the chairman default, the council digest and the rank test are fixed.
**Created:** 2026-10-08 · **Status:** done

## Tasks
- [x] 1. council answers parse from the raw stream when decoding dropped lines — backend/high
      Scope: council/run.go, council/run_test.go
      Accept: a session whose adapter has an `output_decoder`, whose decoded stdout holds the `<<<COUNCIL` and `COUNCIL>>>` markers and the verdict but not the JSON finding lines, and whose raw stdout holds the full block, parses with every finding → go test ./council -run TestRunParsesRawWhenDecodedDropsLines; when decoded and raw stdout both parse and disagree, the session is a council failure with reason naming the conflict → go test ./council -run TestRunDecodedRawConflict; a decoded answer that parses on its own is used as today → go test ./council -run TestRunStages
- [x] 2. one redaction for failure tails in review and council, JSON secret fields included — backend/medium
      Depends on: 1
      Scope: executor/tail.go, executor/tail_test.go, review/report.go, review/report_test.go, council/run.go, council/run_test.go, cmd/batuta/council.go, cmd/batuta/council_test.go
      Accept: an exported helper in `executor/tail.go` drops every line that holds a secret-shaped assignment anywhere or a JSON field whose key names a secret (`token`, `api_key`, `password`, `secret`, case-insensitive), and keeps the other lines → go test ./executor -run TestDropSecretFieldLines; review failure tails use it with their behaviour unchanged → go test ./review -run 'TestReviewFailureTail'; a council session that fails with `{"api_key":"sk-live"}` or `{"token":"tok-example"}` on stdout or stderr leaves neither value in `council.json` or `council.md` → go test ./cmd/batuta -run TestCouncilFailureTailRedacted
- [x] 3. council artefacts are written atomically — backend/medium
      Depends on: 2
      Scope: cmd/batuta/council.go, cmd/batuta/council_test.go
      Accept: each artefact is written to a temporary file in the destination directory and renamed into place only after the write and close succeed, so a failing write leaves the previous `council.json` and `council.md` intact → go test ./cmd/batuta -run TestCouncilArtefactsAtomic; a second run on the same plan and date replaces both artefacts with the new run's content → go test ./cmd/batuta -run TestCouncilArtefactsReplaced
- [x] 4. chairman default, council digest and the rank test — backend/medium
      Scope: routing/table.go, routing/table_test.go, council/aggregate_test.go
      Accept: without a `chairman` row the chairman is the high lane `*` row, else the first high lane row of any domain → go test ./routing -run TestChairmanDefaultDomainRow; the council part of the routing digest changes when only a council row's lane changes → go test ./routing -run TestCouncilDigestIncludesLane; the average-rank test asserts the number of rankings and that critique A exists with its expected average and count → go test ./council -run TestAggregateRankEntries

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`, temp dirs through `tempDir(t)`. `cmd/batuta` must build on linux, darwin and windows. Release tooling is never touched. Tests use fake adapters and fake subprocess runners; no test calls a real model.

Environment setup is never a question. `GOCACHE` is set to a writable directory and the Go 1.26.4 toolchain is first on PATH with `GOTOOLCHAIN=local`: run `go test` plainly. If your shell still has `GOROOT` pointing to the mise Go 1.24.2 install, run `env -u GOROOT go test …`; if that is refused too, finish the code and tests, say so in your report and stop — the conductor's gate runs the suite.

**Task 1.** `runCouncilSession` (`council/run.go` ~205) reads decoded `Result.Stdout` and falls back to `Result.RawStdout` only when decoded stdout is empty; the review found that a decoder can keep the markers and verdict while dropping JSON finding lines it takes for unknown stream events.

**Task 2.** `review/report.go` has `dropEmbeddedSecretLines` and `redactStreamTail` (~354–383) from the earlier fixes; `council/run.go` `councilFailure` (~251) uses only `executor.DropSecretBearingLines`. Move the JSON-aware rule into `executor/tail.go` beside `DropSecretBearingLines` (keep that one as it is) and use it from both packages. Decoding escaped JSON key names stays out of scope, as decided before.

**Task 3.** `review/report.go` `writeArtifact` (~399) is the atomic pattern to follow.

**Task 4.** `RoutingTable.ChairmanRole` (`routing/table.go` ~375) calls `t.Row(ComplexityHigh, DomainAny)`; a valid table may have only domain-specific high rows. The council rows are parsed around ~191 and the role digest is built in the same file.
