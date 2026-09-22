# Plan — structured-output review fixes 2: the session owns the not_applicable decision, drift still enforces effort
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Close the two majors and the minor of the second review of branch `feat/structured-output-impl` (verdict FIX_BEFORE_SHIP, `.batuta/reviews/2026-09-22-structured-output-fixes/`): the receipt says `not_applicable` only when the session actually skipped the effort, and a configuration update during the prompt still rejects a lost effort.
**Created:** 2026-09-22 · **Status:** done

## Tasks
- [x] 1. The session records whether it skipped the effort; drift during the prompt keeps enforcing it — backend/high
      Scope: executor/acp/session.go, executor/acp/session_test.go
      Accept: acp.Session exposes EffortNotApplicable() bool, true only when NewSession skipped a requested effort because no EffortConfigID was declared and the session advertised no thought_level option, false when the effort was selected or none was requested → go test ./executor/acp -run TestSessionEffortNotApplicableWithoutThoughtLevel; after a successful selection of effort high through a thought_level option, a config_option_update during Prompt that drops the thought_level option fails the prompt as configuration drift → go test ./executor/acp -run 'TestSessionRejectsConfigurationDriftDuringPrompt/effort_option_dropped'; a session that skipped the effort at creation does not fail when a later config_option_update still carries no thought_level option → go test ./executor/acp -run 'TestSessionRejectsConfigurationDriftDuringPrompt/skipped_effort_stays_skipped'; the package stays green → go test ./executor/acp
- [x] 2. The receipt's not_applicable effort comes from the session, never from the adapter alone — backend/high
      Depends on: 1
      Scope: executor/acp_backend.go, executor/acp_backend_test.go, executor/transport.go, executor/transport_test.go, docs/dispatch.md
      Accept: ACPBackend.Execute sets Receipt.Effort to not_applicable exactly when the session's EffortNotApplicable() is true, and TransportBackend.Execute no longer overwrites Receipt.Effort → go test ./executor -run TestQualificationEffortNotApplicable; with no acp_effort_config and a session that advertises and confirms a thought_level option for the requested effort, Receipt.Effort is not not_applicable → go test ./executor -run TestACPReceiptEffortSelectedByCategory; when NewSession fails with ErrConfiguration on an offered but unsupported effort, Receipt.Effort is not not_applicable → go test ./executor -run TestACPReceiptEffortRejectedNotStamped; docs/dispatch.md says the receipt records not_applicable only when the session skipped the effort → grep -q 'only when the session skipped' docs/dispatch.md; the packages stay green → go test ./executor ./executor/acp

## Decisions and context

Go standard library only, conventional commits. Nothing here changes routing, gates or outcomes.

**Task 1.** Today `checkConfiguration` (session.go ~215) swallows the effort error through `effortNotApplicable` on both the `NewSession` pass and the enforcing pass inside `Prompt` (~434). Record the skip in a session field during `NewSession` (the `selectOption` call ~112 is where it is decided), expose it through `EffortNotApplicable()`, and on the enforcing pass skip the effort check only when that field is true and the update still advertises no thought_level option; any other effort mismatch returns `ErrConfiguration`. Add the two drift cases as subtests of the existing `TestSessionRejectsConfigurationDriftDuringPrompt` (read it first for its structure; the second case expects success, so branch on an expected error in the table). Extend the existing `TestSessionEffortNotApplicableWithoutThoughtLevel` to assert `EffortNotApplicable()`.

**Task 2.** Remove the stamp in `transport.go` ~168 (`if result.Receipt != nil && e.Adapter.ACP != nil && e.Adapter.ACP.EffortConfigID == "" …`). In `acp_backend.go` the session is created ~90; set `receipt.Effort = "not_applicable"` there from `session.EffortNotApplicable()`. `TestQualificationEffortNotApplicable` in `transport_test.go` (~503) must still pass through the new path; its fake session must advertise no thought_level option. Reuse the fake ACP peers already in `acp_backend_test.go`.
