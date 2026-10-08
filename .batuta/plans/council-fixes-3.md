# Plan — batuta council: prose before the critique block, and majors with majority support
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** The second real run of `batuta council` (2026-10-08) lost codex's correct critique because one sentence of prose came before the `<<<COUNCIL` block, and recommended APPROVE although three major findings had the support of every parsed counsellor. The critique parser accepts prose before the block, and a major finding with majority support also makes the recommendation REVISE.
**Created:** 2026-10-08 · **Status:** approved

## Tasks
- [ ] 1. the critique parser accepts prose before the block — backend/medium
      Scope: council/parse.go, council/parse_test.go, council/testdata/codex-critique.txt
      Accept: the recorded codex critique, copied verbatim into `council/testdata/codex-critique.txt`, parses with all its findings and verdict REVISE despite the sentence before `<<<COUNCIL` and the stdin notice after the verdict → go test ./council -run TestParseCritiqueLeadingProse; a critique with two `<<<COUNCIL` blocks, or with text between `COUNCIL>>>` and the verdict that is not blank, still fails → go test ./council -run TestParseCritiqueStructure; the earlier recorded answers still parse → go test ./council -run TestParseRecordedAnswers
- [ ] 2. a major finding with majority support makes the recommendation REVISE — backend/medium
      Depends on: 1
      Scope: council/aggregate.go, council/aggregate_test.go, docs/council.md
      Accept: the aggregate recommends REVISE when any blocker or major finding has the support of more than half of the parsed critiques, or more than half of them said REVISE, and APPROVE otherwise; minor findings never change the recommendation → go test ./council -run TestAggregateMajorMajority; the existing aggregate behaviour for blockers and verdicts stays → go test ./council -run TestAggregate; docs/council.md states the rule with majors → grep -q 'major' docs/council.md

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`. Release tooling is never touched. Tests use fixtures and fake runners; no test calls a real model.

Environment setup is never a question. `GOCACHE` is set to a writable directory and the Go 1.26.4 toolchain is first on PATH with `GOTOOLCHAIN=local`: run `go test` plainly. If your shell still has `GOROOT` pointing to the mise Go 1.24.2 install, run `env -u GOROOT go test …`; if that is refused too, finish the code and tests, say so in your report and stop — the conductor's gate runs the suite.

Decided with the maintainer on 2026-10-08: majors with majority support count like blockers for the recommendation.

**Task 1.** The recorded answer is `.batuta/council-smoke/2026-10-08/codex-critique.txt`; copy it byte for byte into `council/testdata/codex-critique.txt`. `ParseCritique` is in `council/parse.go`; the tolerance added for trailing lines after the verdict stays. Prose before the block is ignored, but the block itself, its JSON lines and the verdict keep their exact form.

**Task 2.** `Aggregate` (`council/aggregate.go` ~40–95) sets REVISE when a blocker's support is more than half of the critiques (~89) or more than half of the verdicts are REVISE (~93).
