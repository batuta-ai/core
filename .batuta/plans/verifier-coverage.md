# Plan — verifier coverage: no criterion passes unchecked, no verifier on an attempt already rejected
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Close the coverage hole the independent review found: a criterion without a proof passes with "left to the verifier", yet on a low or medium lane's first execution the verifier is never dispatched, so the criterion passes unchecked (13 passing reports, 21 such criteria in the journals). Then stop spending a verifier session on an attempt whose tests, scope or proofs have already rejected it (10 of 87 verifier-bearing reports). Code only; no judge involved.
**Created:** 2026-09-22 · **Status:** approved

## Tasks
- [x] 1. A criterion without a proof always gets the verifier — backend/medium
      Scope: gates/gates.go, gates/gates_test.go, loop/attempt.go, loop/verifier_policy_test.go
      Accept: gates.NeedsVerifier takes a fourth argument, whether any criterion has no proof command, and returns true when it does, whatever the lane and execution → go test ./gates -run TestNeedsVerifierProoflessCriterion; the existing lane, silent-tree and retry cases keep their answers → go test ./gates -run TestNeedsVerifier; on a medium-lane first execution whose plan has one criterion without a proof, the loop dispatches the verifier and the report carries its verdict → go test ./loop -run TestProoflessCriterionDispatchesVerifier; on a medium-lane first execution where every criterion has a proof, no verifier runs → go test ./loop -run TestAllProvenCriteriaSkipVerifierOnMedium; the packages stay green → go test ./gates ./loop
- [ ] 2. No verifier session on an attempt already rejected by tests, scope or a proof — backend/medium
      Depends on: 1
      Scope: loop/attempt.go, loop/verifier_policy_test.go, docs/loop.md
      Accept: when the tests gate, the scope gate or any criterion proof has failed, the loop does not dispatch the verifier, leaves Report.Verifier nil and records verifier_skipped with the failing gate names in the gates_reported detail → go test ./loop -run TestRejectedAttemptSkipsVerifier; when those gates pass the verifier still runs exactly as task 1 requires → go test ./loop -run TestProoflessCriterionDispatchesVerifier; the attempt outcome and its retry feedback are unchanged apart from the missing verifier lines → go test ./loop -run TestRejectedAttemptKeepsOutcome; docs/loop.md states when the verifier runs and when it is skipped → grep -q 'verifier_skipped' docs/loop.md; the package stays green → go test ./loop

## Decisions and context

Go standard library only, conventional commits. `gates.NeedsVerifier` has exactly one production caller, `loop/attempt.go`, and one test file, `gates/gates_test.go`; both are in Scope. Put the new loop tests in the new file `loop/verifier_policy_test.go`, driving a fake executor and a fake verifier the way the existing loop tests do. The silent-tree path (no change, `already_satisfied` checks) is not touched.

**Task 1.** A criterion has no proof when `gates.Criterion.Proof` is empty; that is the case `gates.Proofs` marks "no proof command; left to the verifier". Compute the flag in `loop/attempt.go` from `criteria` and pass it.

**Task 2.** Decide the skip after `gates.Tests`, `gates.Scope` and `gates.Proofs` have run and before `r.verify`. The skip reason lists the failing gate names (`tests`, `scope`, `proof N`). The attempt is already failing, so `Decide()` is unaffected; do not change `Decide`.
