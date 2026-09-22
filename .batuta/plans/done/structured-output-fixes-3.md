# Plan — structured-output review fixes 3: decoded output bounds proven, a failed write drops its line, qualification wording exact
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Close the findings of the third review of branch `feat/structured-output-impl` (verdict FIX_BEFORE_SHIP, `.batuta/reviews/2026-09-22-structured-output-fixes-2/`): a test proves the decoded and raw stdout stay bounded, the decode writer never re-emits a line after a failed write and never buffers an unbounded partial line, and the qualification docs say what `matches()` checks versus what the session checks after launch.
**Created:** 2026-09-22 · **Status:** done

## Tasks
- [x] 1. The decode writer is bounded and drops a line whose write failed — backend/medium
      Scope: executor/run.go, executor/run_test.go
      Accept: with an adapter that declares output_decoder, an execution whose observer chunks and CommandResult.Stdout exceed outputLimit yields len(RawStdout) == outputLimit, len(Stdout) <= outputLimit and Truncated true → go test ./executor -run TestRunDecodedOutputBounded; when the destination writer fails on a decoded line, that line is removed from the pending buffer, so a later Write or flush does not decode or retain it again → go test ./executor -run TestDecodeWriterDropsLineAfterWriteError; a partial line with no newline never makes the pending buffer grow beyond outputLimit, and the excess sets truncated → go test ./executor -run TestDecodeWriterBoundsPendingLine; the package stays green → go test ./executor
- [x] 2. Qualification docs separate launch matching from session checks — docs/low
      Scope: docs/dispatch.md, executor/transport.go
      Accept: the ACPQualification doc comment in executor/transport.go says that Model "*" matches any requested model and that an empty qualification Effort matches any requested effort when the adapter declares no acp_effort_config → grep -q 'empty qualification Effort matches any requested effort' executor/transport.go; docs/dispatch.md says that launch matching checks only the qualification fields, and that advertising and confirming the model and skipping the effort happen in the session after launch → grep -q 'in the session after launch' docs/dispatch.md; the package stays green → go test ./executor

## Decisions and context

Go standard library only, conventional commits. Nothing here changes routing, gates or outcomes. Task 2 edits only a comment in `executor/transport.go`, no code.

**Task 1.** `decodeWriter.Write` (run.go ~302) returns on an `emit` error before advancing `w.pending`, and `Execute` always calls `flush()` after `Run`, so the same line is decoded and retained twice. Advance `pending` past the line before returning the error. `pending` grows without limit while no newline arrives; cap it at `outputLimit`, drop the excess and set `truncated`. `boundCopy` (~365) already bounds `RawStdout`; the new test proves it together with the decoded side. Reuse the fake runner and adapters already in `run_test.go` (`TestRunDecodesStreamOutput` ~68 is the closest model).

**Task 2.** Facts to state: `ACPQualification.matches` (transport.go ~97–103) accepts any requested model when `Model` is `*`, and any requested effort when the qualification's `Effort` is empty and `Adapter.ACP.EffortConfigID` is empty; it does not look at session options. The model check (advertised among options, confirmed before the prompt) and the effort skip (`Session.EffortNotApplicable()`) happen in `acp.NewSession`, after launch. Rewrite docs/dispatch.md ~55–60 accordingly; keep the release facts (OpenCode 1.18.31, `opencode acp`, darwin/arm64).
