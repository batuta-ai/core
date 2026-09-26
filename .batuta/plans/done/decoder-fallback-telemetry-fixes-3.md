# Plan — decoder-fallback-telemetry review fixes 3: pruning stays inside the guard, every failing verifier detail is redacted
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the findings of the final claude/opus branch review (verdict FIX_BEFORE_SHIP, 3/3 cohorts): pruning stale cohort tails changes the tree outside what the review publication guard excludes, only one failing verifier signal is redacted, and two small grammar and boundary mismatches.
**Created:** 2026-09-25 · **Status:** done

## Tasks
- [x] 1. Stale tails are excluded from the guard before they are pruned; failing verifier details are always redacted — backend/medium
      Scope: cmd/batuta/main.go, cmd/batuta/main_test.go, review/report.go, review/report_test.go, executor/tail.go, executor/tail_test.go, loop/attempt.go, loop/verifier_policy_test.go, gates/gates.go, gates/gates_test.go
      Accept: the review publication guard excludes every `cohort-*.tail.txt` present in the artifact directory before the review runs as well as the ones the report produces, so an incremental review that prunes a stale tail passes the tree guard → go test ./review -run TestIncrementalReviewPrunesStaleTailInsideGuard && go test ./cmd/batuta -run TestReviewGuardExcludesStaleTails; every failing verifier verdict's Detail, not only the no-TASK-lines one, goes through executor.RedactPaths and executor.DropSecretLines → go test ./loop -run TestVerifierFailureDetailAlwaysRedacted; gates exports HasTaskLines built on the same regex gates.Verifier parses, and loop uses it instead of its own looser regex → grep -q 'func HasTaskLines' gates/gates.go && grep -q 'gates.HasTaskLines' loop/attempt.go && go test ./gates -run TestHasTaskLines; workspace stripping treats the trailing punctuation RedactPaths trims (`.`, `,`, `;`, `:`, `)`) as a boundary → go test ./executor -run TestRedactWorkspaceBoundary; the packages stay green → go test ./executor ./review ./loop ./gates ./cmd/batuta

## Decisions and context

Go standard library only, conventional commits. The guard is built in `cmd/batuta/main.go` (~1186–1200) from `review.ArtifactPaths`; list the existing tail files before the run and add them. Pruning is `pruneStaleTails` in `review/report.go` (~229). The verifier is `(*Runner).verify` in `loop/attempt.go` (~700–770). Sandbox note: the sandbox blocks the Go build cache and loopback; write code and tests, do not work around the sandbox, and let the conductor's gate run the proofs.
