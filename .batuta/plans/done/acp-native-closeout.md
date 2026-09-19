# Plan — native macOS ACP closeout

**Goal:** Make optional ACP usable through stock dispatch and loop on qualified native macOS routes, using the explicitly approved managed-process-group lifecycle contract. Preserve CLI defaults, exact model/effort, permission rejection, independent gates and uncertain-work reconciliation.
**Created:** 2026-09-17 · **Status:** done

## Tasks
- [x] 1. Verify owned process-group shutdown on native macOS — backend/high
      Scope: executor/acp/process.go, executor/acp/process_unix.go, executor/acp/process_windows.go, executor/acp/process_test.go, executor/acp/process_unix_test.go, executor/acp/process_windows_test.go, executor/acp_backend_test.go, executor/transport.go, docs/dispatch.md
      Accept: ordinary and noncooperative real process groups drain within bounds and unrelated processes survive, observed escaped survivors are not reported clean → go test ./executor/acp -timeout 3m; process lifecycle race suite passes → go test -race ./executor/acp -timeout 3m; existing executor behavior remains compatible → go test ./executor/... -timeout 5m
- [x] 2. Connect the native lifecycle and reject-by-default permissions to stock commands — backend/high
      Depends on: 1
      Scope: executor/acp_native.go, executor/acp_native_test.go, executor/transport.go, executor/transport_test.go, cmd/batuta/main.go, cmd/batuta/main_test.go, docs/dispatch.md
      Accept: production factory starts the resolved fixed invocation in the exact workspace and owns cleanup on every outcome, new permission requests reject by default → go test ./executor/... -timeout 5m; stock CLI uses the factory while unavailable routes and default CLI behavior remain unchanged, no adapter can self-qualify → go test ./cmd/batuta -timeout 5m
- [x] 3. Qualify exact native routes with real evidence and document operational limits — backend/high
      Depends on: 2
      Scope: executor/acp_native.go, executor/acp_native_test.go, executor/transport_test.go, cmd/batuta/main_test.go, docs/dispatch.md, docs/dispatch-measurement.md, docs/loop.md, README.md, README.pt-BR.md, .batuta/runs/acp-native-qualification/**
      Accept: record exact executor version/model/effort/platform only after disposable real task, permission rejection and cancellation/group cleanup evidence; candidate qualification and all regressions pass → go test ./executor/... ./cmd/batuta ./loop -timeout 15m; all supported targets remain buildable → go build ./...

- [x] 4. Resolve accepted final-review lifecycle and coverage findings — backend/high
      Depends on: 3
      Scope: executor/acp/process_unix.go, executor/acp/process_unix_test.go, cmd/batuta/main_test.go, executor/transport_test.go, README.md, README.pt-BR.md, docs/loop.md, docs/dispatch.md, docs/dispatch-measurement.md
      Accept: verified group disappearance gives direct-child reaping its own bounded wait, already-reaped children do not race an expired stage timer and vanished groups are never signalled again → go test ./executor/acp -timeout 3m; exact constructor selection has a platform-independent positive fixture and public native dispatch has actual launch evidence while foreign platforms stay rejected → go test ./executor ./cmd/batuta -timeout 5m; docs name all unsupported architectures without weakening discovery uncertainty → go test ./... -timeout 15m

## Decisions and context

The user approved native macOS as required, then explicitly agreed to the Compozy-style managed-group contract. This supersedes the former arbitrary-descendant containment requirement. A successful drain means the owned group is gone; it cannot prove absence of all processes that escaped into other groups. Known cleanup failures/observed escapes remain visible, with no replay of uncertain work. No Endpoint Security, new privileged helper, daemon, VM, package installation, global configuration changes or new dependencies are included. Do not edit Compozy. Existing released APIs remain stable. Documents are English, with existing README Portuguese mirror maintained.

Routes stay exact and release-owned; adapter metadata alone is not qualification. No fake-peer unit test proves a real provider qualifies. First candidates are already-installed OpenCode and Cursor; qualify only actual passing combinations, do not claim all providers or platforms. No paid account changes or automatic wrapper install. Default ACP permission callback rejects new requests; provider-side existing permission configuration remains the provider's responsibility, not a newly invented filesystem sandbox. No broad approval flags or new implicit allow policy.

Implementation is sequential through the Batuta cycle; use isolated feat/acp-production at origin/main2189395. After these core tasks: complete approved skills ACP plan against actual command contract, measured host pilot with honest unknown token counters, Fable delivery review, PR/CI, authorized releases and official host sync/pin. No release is closed merely by implementation completion.

Final-review judgment: process discovery failures during tracking remain sticky uncertainty, including when later snapshots succeed. Missed observation intervals cannot be reconstructed. This intentional conservative behavior is not relaxed for availability. Accepted review work fixes the independent stage/reaping timer race, strengthens test coverage, and clarifies unsupported architectures. A final Fable report must have complete cohort coverage; the first round had invalid framing in one cohort and is not a release approval.

2026-09-18 correction amendment: user approved implementation of the final-review judgment. Task 4 also corrects test-owned cross-platform negative-path coverage and qualification provenance in docs/dispatch-measurement.md. Preserve all strict cleanup/EOF assertions and existing production timing. Refresh real-provider probes against the final lifecycle revision; historical evidence remains retained. Judgment: .batuta/worktrees/acp-production-delivery/.batuta/runs/acp-fable-final-judgment.md.

Completed 2026-09-18: PR #90 merged at 3102c845631a0ff13975da7d1861f9720d727935 after complete review coverage, conductor judgment, native requalification, and green Linux CI. Release/skills/host follow-ups remain outside this native-closeout plan.
