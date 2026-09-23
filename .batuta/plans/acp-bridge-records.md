# Plan — qualification records for the codex and claude ACP bridges
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Record the release-owned qualifications that the requalification on 2026-09-23 established (`.batuta/acp-probes/qualify-2026-09-23/final/README.md`): codex-acp 1.13.1 in session mode `read-only` and claude-agent-acp 0.81.1 in mode `acceptEdits` with the sandbox session meta, both on darwin/arm64 under the worktree permission policy. A qualification also pins the mode and session meta it was proven with, so an adapter that changes either stops matching.
**Created:** 2026-09-23 · **Status:** approved

## Tasks
- [ ] 1. A qualification pins the session mode and meta, and the stock factory carries the codex and claude records — backend/high
      Scope: executor/transport.go, executor/transport_test.go, executor/acp_native.go, executor/acp_native_test.go
      Accept: ACPQualification gains Mode and SessionMeta; matches requires the adapter's ACP.Mode to equal Mode and the adapter's SessionMeta to equal SessionMeta after both are compacted with json.Compact, and an empty Mode or SessionMeta matches only an adapter that declares none → go test ./executor -run TestQualificationPinsModeAndMeta; NewNativeTransport keeps the OpenCode record unchanged and adds {Executor codex, Run codex-acp, Version "@agentclientprotocol/codex-acp 1.13.1", darwin/arm64, Model *, Effort "", Mode read-only} and {Executor claude, Run claude-agent-acp, Version "0.81.1", darwin/arm64, Model *, Effort "", Mode acceptEdits, SessionMeta {"claudeCode":{"options":{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}}}}, each with Permissions, Cleanup, AuthenticatedTask and Platform true → go test ./executor -run TestNativeTransportBridgeRecords; an adapter for codex without acp_mode, or for claude without the session meta, is not qualified → go test ./executor -run TestNativeTransportBridgeRecords; the packages stay green → go test ./executor ./executor/acp
- [ ] 2. The measurement doc records the bridge qualifications — docs/medium
      Depends on: 1
      Scope: docs/dispatch-measurement.md, docs/dispatch.md
      Accept: docs/dispatch-measurement.md gains a section "Codex and Claude bridge qualification" with the two exact tuples (launch, `--version` output, entry SHA-256, mode, session meta, platform), the eight cases with their observations and durations, and the limits (managed-group contract, model `*` because the evidence is about the bridge process and not the model, CLI JSON unchanged for cursor and agy) → grep -q 'Codex and Claude bridge qualification' docs/dispatch-measurement.md; docs/dispatch.md no longer says only the OpenCode tuple has accepted native qualification evidence and names the three qualified bridges → grep -q 'claude-agent-acp' docs/dispatch.md && ! grep -q 'Only the exact OpenCode tuple above has accepted native qualification evidence' docs/dispatch.md; the package stays green → go test ./executor

## Decisions and context

Go standard library only, conventional commits. Nothing here changes routing, gates or outcomes; the adapters that select these bridges come in a skills PR after this plan, and until then no adapter matches the new records.

**Task 1.** `ACPQualification` and `matches` are in `executor/transport.go` (~73–108); `ACPLaunch` there already has `Mode` and `SessionMeta` (`json.RawMessage`). `NewNativeTransport` is in `executor/acp_native.go` (~14). Model `*` follows the OpenCode record: the lifecycle evidence is about the bridge process, and the session still checks that the model is advertised and confirmed. Effort `""` matches any requested effort only while the adapter declares no `acp_effort_config`; both bridges select effort by the `thought_level` category.

**Task 2.** Copy the facts from `.batuta/acp-probes/qualify-2026-09-23/final/README.md` (tuples, table, limits); keep the existing OpenCode sections unchanged. In docs/dispatch.md the sentence to replace is "Only the exact OpenCode tuple above has accepted native qualification evidence." (~134).
