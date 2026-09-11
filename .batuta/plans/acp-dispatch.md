# Plan — optional external ACP execution
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:d8b592b68c43 -->

**Goal:** Add an opt-in ACP client for external executors, shared by loop and an interactive dispatch command, with bounded receipts, safe permissions, verified shutdown and no replay of uncertain work. Preserve legacy CLI behavior and public consumer signatures.
**Created:** 2026-09-11 · **Status:** approved

## Tasks
- [ ] 1. Introduce a backend seam without changing CLI execution — backend/high
      Scope: executor/run.go, executor/backend.go, executor/backend_test.go, executor/run_test.go, loop/runner.go, loop/attempt.go, loop/attempt_test.go
      Accept: default backend preserves execution, progress, logs, gate inputs and current CLI outcomes → go test ./executor ./loop; existing public signatures and all targets remain buildable → go build ./...
- [ ] 2. Add bounded receipts and honest optional usage — backend/medium
      Depends on: 1
      Scope: executor/receipt.go, executor/receipt_test.go, executor/usage.go, executor/usage_test.go, executor/run.go
      Accept: receipt separates submitted state, transport outcome and worker claim, bounds size with visible overflow and evidence references → go test ./executor; missing usage is unknown and cached/input/output counters are not double counted → go test ./executor
- [ ] 3. Implement bounded ACP JSON-RPC communication — backend/high
      Scope: executor/acp/connection.go, executor/acp/connection_test.go, executor/acp/messages.go, executor/acp/testdata/**
      Accept: scripted peers prove initialize negotiation, concurrent response correlation and notification handling → go test ./executor/acp; malformed or oversized frames, missing replies, EOF and unknown requests terminate within test deadlines without unbounded buffers → go test -race ./executor/acp
- [ ] 4. Implement configured ACP task sessions and result mapping — backend/high
      Depends on: 1, 2, 3
      Scope: executor/acp/session.go, executor/acp/session_test.go, executor/acp/messages.go, executor/acp/testdata/**, executor/acp_backend.go, executor/acp_backend_test.go
      Accept: session uses exact absolute cwd and acknowledged model/effort before prompt, incompatible config stops before submission → go test ./executor/...; streaming messages and usage map to receipts while non-success stop reasons and errors never become Finished success → go test ./executor/...
- [ ] 5. Enforce ACP permission outcomes without implicit approval — backend/high
      Depends on: 4
      Scope: executor/acp/permission.go, executor/acp/permission_test.go, executor/acp/session.go, executor/acp/session_test.go, executor/acp/testdata/**, executor/acp_backend.go, executor/acp_backend_test.go
      Accept: allow only a supplied explicit policy and reject or return a structured question otherwise, with rejected target absent in fixture → go test ./executor/...; end_turn or success prose after rejection cannot erase the rejection and unsupported client methods are denied → go test ./executor/...
- [ ] 6. Verify cancellation and owned subprocess shutdown — backend/high
      Depends on: 4
      Scope: executor/acp/process.go, executor/acp/process_unix.go, executor/acp/process_windows.go, executor/acp/process_test.go, executor/acp/process_unix_test.go, executor/acp/process_windows_test.go, executor/acp/session.go, executor/acp/session_test.go, executor/acp_backend.go, executor/acp_backend_test.go
      Accept: running-child cancellation, ignored cancel, ignored EOF, child group changes and late results have bounded shutdown or explicit unresolved cleanup → go test -race ./executor/...; cancellation acknowledgement never alone authorizes cleanup and reused or unrelated process identities are not killed → go test ./executor/...
- [ ] 7. Select qualified ACP backends while preserving CLI adapters — backend/high
      Depends on: 4, 5, 6
      Scope: executor/adapter.go, executor/adapter_test.go, executor/transport.go, executor/transport_test.go, executor/acp_backend.go, executor/acp_backend_test.go, executor/testdata/**
      Accept: existing adapter files and omitted transport retain CLI, explicit unsupported ACP fails and auto falls back only before prompt submission → go test ./executor/...; executable availability, observed protocol/config capabilities and qualification gate eligibility without a model probe or automatic install → go test ./executor/...
- [ ] 8. Wire optional ACP into attempts and independent verification — backend/high
      Depends on: 7
      Scope: loop/runner.go, loop/attempt.go, loop/attempt_test.go, loop/loop_test.go, loop/brief.go, loop/brief_test.go, gates/gates.go, gates/gates_test.go
      Accept: fake ACP delivery passes the existing finished/tree/tests/scope/proofs/verifier pipeline and CLI scenarios remain unchanged → go test ./loop ./gates; ACP tool updates never fabricate criterion DONE and raw streams do not replace compact status → go test ./loop ./gates
- [ ] 9. Persist dispatch identity and prevent uncertain replay — backend/high
      Depends on: 8
      Scope: loop/attempt.go, loop/attempt_test.go, loop/runner.go, loop/settle.go, loop/settle_test.go, loop/report.go, loop/report_test.go, loop/loop_test.go
      Accept: crash boundaries before/after prompt submission preserve backend/model/workspace/brief identity and old CLI journals resume → go test ./loop; disconnect, quota error or unknown completion after submission parks ACP work without automatic transport/model replay and without consuming or deleting unverified work → go test ./loop
- [ ] 10. Expose bounded dispatch for interactive skills — backend/high
      Depends on: 7, 9
      Scope: cmd/batuta/main.go, cmd/batuta/main_test.go, executor/dispatch.go, executor/dispatch_test.go
      Accept: batuta dispatch accepts brief-file, explicit executor/model/effort, cwd and transport, returns compact JSON with artifact references and accurate exit class → go test ./cmd/batuta ./executor/...; loop accepts transport selection, default stays CLI and headless native requests are never silently treated as available → go test ./cmd/batuta ./loop; missing binary or invalid arguments cause no install or model call → go test ./cmd/batuta
- [ ] 11. Document compatibility and measure transport outputs — docs/medium
      Depends on: 10
      Scope: docs/dispatch.md, docs/dispatch-measurement.md, docs/loop.md, README.md, README.pt-BR.md, executor/receipt_test.go, executor/acp_backend_test.go
      Accept: docs distinguish native host dispatch from ACP sessions, opt-in flags, uncertain execution, per-executor qualification and CLI rollback; examples and receipt fixtures preserve model/usage provenance and unknown counters → go test ./executor/...; full regression suite passes → go test ./...; module builds → go build ./...

## Decisions and context

This plan follows host `.batuta/dispatch-design.md` and `runs/acp-authenticated-experiment.md`. All task numbers are local to this repository. Routing predicts codex/gpt-6-astra with high reasoning for high tasks, codex/gpt-5.6-sol for medium tasks. The profile's standard-library-only description is stale: go.mod already has UI/TOML/YAML dependencies; do not add a new protocol framework or replace dependencies as part of this work. Public consumer signatures named in profile remain frozen. No changes to archived batuta-cli, Compozy repositories, release manifests or workflows.

Before execution reconcile main/remote reality, preserve existing state and use isolated worktrees. This plan was approved by the user on 2026-09-11. A task needing scope beyond its listed files must stop for a conductor re-brief. New Go files may be consolidated into fewer paths within Scope; do not create abstractions solely because filenames are listed. Core review's separate cohort driver may retain CLI in this delivery, with its behavior explicit in docs. Task verifier path inside the loop must remain independent even when using ACP.

**Task 1.** executor.Result currently has no provider usage or submission certainty. Runner holds a concrete executor.Subprocess and mutates its progress/stdout/stderr fields per attempt. Preserve those semantics behind a small per-invocation boundary, not a plugin framework. publication.CommandRunner is a bounded one-shot runner with finite stdin; do not globally change its behavior to implement bidirectional ACP.

**Task 2.** Proposed compact receipt limit is 4 KiB. Preserve essential failure state plus a complete owned artifact reference when details overflow. Do not conflate a worker result with proof verification. Usage fields are nullable/optional with provenance; no bytes/4 presented as exact tokens. Do not repurpose existing graph token accounting whose semantics belong to another consumer. Prefer additive result metadata.

**Tasks 3–6.** Implement a minimal stdio ACP v1 client with negotiated capabilities, not an ACP server. Advertise only implemented client methods. One process/session per isolated attempt initially; no cross-task session pool or optional nested-subagent extensions. Keep framing, pending requests, output and shutdown bounded. Never log credentials or inject raw reasoning streams into conductor context. Reuse existing redaction and limits where applicable. Tests use controlled local peers and real bounded child fixtures, not paid model calls.

**Task 5.** No user approval may be inferred from model prose, elapsed time or the availability of an allow option. A queued permission question carries only safe actionable information and existing task identity. Unattended execution must reject/park when its existing authorization does not cover the action. Do not introduce blanket approval to make external agents work.

**Task 6.** A process group is insufficient when descendants change groups. Track owned process identities and use platform-specific facilities without affecting unrelated processes. Native Windows runtime coverage is required before declaring Windows ACP qualified; cross-compilation alone is not that proof. Until such evidence exists, explicit unavailable/CLI behavior is acceptable and must be tested. No permanent orphan claim is inferred from the current Codex 4.1s observation. Test hard timeout against a peer that ignores cancel, distinct from the prior cooperative timer probe.

**Task 7.** Add optional ACP launch metadata to existing adapters, no new executor IDs or route rows. Current providers remain themselves across transports. Dedicated Codex/Claude wrapper versions are explicit prerequisites, never downloaded implicitly. OpenCode and Cursor are pilot candidates, not universally qualified. Codex permission callback and cleanup, Claude authenticated task execution and platform-specific tests are release gates. Agy retains its CLI path. The old CLI route remains selectable even for an ACP-qualified executor.

**Tasks 8–9.** Preserve gate evidence and candidate verification. New journal detail must be additive and tolerate old entries with implicit CLI. Record an intent before sending a prompt and classify a crash in the send/ack window as uncertain. The current rate-limit loop can rerun/switch runtimes; ACP must not enter that behavior after possible submission. Protocol error/rate-limit status is not proof that no mutations occurred. Resume parks uncertain work for reconciliation rather than regenerating a prompt or deleting its worktree. Do not change legacy CLI limit policy as an unrelated behavior change.

**Task 10.** Proposed interface: batuta dispatch --brief-file <path> --executor <id> --model <id> [--effort <value>] --cwd <worktree> --transport cli|acp|auto. Omitted transport means cli. The command executes one bounded attempt and reports execution facts, not verified acceptance. Interactive skills continue to run existing gates separately. Own receipt/log paths securely and do not overwrite arbitrary files. Standalone command interruption returns uncertainty with retained evidence instead of replaying.

**Task 11.** Measurement protocol uses matched snapshots/models/criteria and separates conductor tokens, worker usage, cached tokens, retries and estimates. Actual paired pilot and savings verdict are owned by the host release plan. No performance dashboard or extra LLM summarizer. Explain rollback to CLI for future attempts while retaining reconciliation requirements for in-flight ACP attempts.
