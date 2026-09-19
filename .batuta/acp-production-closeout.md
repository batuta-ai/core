# ACP production closeout — 2026-09-17

## Current status (supersedes historical investigation below)

The user approved native macOS managed-process-group cleanup, matching Compozy's operational boundary. Endpoint Security/helper requirements are withdrawn. Lifecycle task1 is accepted at debfdec; production constructor/dispatch-loop task2 is accepted at76da074 in feat/acp-production. Parent full core suite passes. Real native qualification probes for OpenCode1.18.31, opencode/big-pickle, empty effort, darwin/arm64 passed task, permission rejection, live-child cancellation and live-child deadline. Five real attempts include one inconclusive command-format probe, retained. Exact qualification registration/docs task3 is in progress; no PR/release is complete yet. Other providers/platforms remain CLI. Source skills integration, matched pilot, Fable review, PR/CI and releases remain pending. Latest user separately authorized local installation: official host0.6.6/skills0.10.1/corebeta23 was installed and verified, excluding the unreleased ACP candidate.

The following sections record the investigation history; their earlier blockers are not current requirements.

## Confirmed state

The user authorized resuming the remaining ACP integration. The approved hybrid dispatch design remains authoritative: CLI default, ACP opt-in, exact route/model/effort, no replay after uncertain submission, no invented permission approval or qualification. Fresh delivery branch feat/acp-production is based on origin/main 2189395 in .batuta/worktrees/acp-production-delivery. No product changes yet. Baseline `go test ./executor/... ./cmd/batuta -timeout 5m` passed (executor 5.701s, acp 1.822s, CLI 44.018s). Scout report: .batuta/scout/2026-09-17-acp-production.md.

Stock CLI constructs TransportBackend with Mode only. ACP.Open, PermissionPolicy and qualification records are missing. StartProcess/Shutdown always returns ErrCleanupUnresolved; Windows rejects startup. Merely wiring callbacks or setting qualification booleans would not complete integration.

## Material platform decision

User decision: native macOS ACP is required in this delivery; Linux-only qualification does not satisfy acceptance. Linux cgroup v2 supplies tree-level kill and populated-state primitives; availability and delegation still need real verification before selecting an implementation. No container, daemon, privileged helper, installation or global configuration change has been authorized by this question. Native macOS requires an independently validated lifecycle approach; process-table polling and a process-group kill do not establish the approved descendant guarantee. Apple's current event.h notes that NOTE_FORK does not expose the child PID in the actual kevent, so FreeBSD NOTE_TRACK behavior must not be assumed on Darwin. Do not weaken cleanup requirements simply to call this delivery done.

Sources: https://cdn.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html and https://raw.githubusercontent.com/apple-oss-distributions/xnu/main/bsd/sys/event.h .

## Ordered remaining delivery

First implement and verify the selected lifecycle with real child fixtures, including noncooperative cancellation, group changes, unrelated-process preservation and bounded cleanup. Then wire an explicit permission policy and release-owned qualification through dispatch/loop; preserve CLI defaults and uncertain-attempt recovery. Run actual disposable tasks and permission/cancellation probes on exact eligible executor versions before adding qualifications. Next complete source-skills ACP integration, matched measurement with unknown counters reported honestly, Fable review, PRs and authorized releases, and host vendoring/pinning through existing scripts.

Observed installed versions now differ from historical probes: OpenCode 1.18.31 and Cursor 2026.09.15-d2fe57e. Dedicated codex-acp and claude-agent-acp are absent from PATH; no wrapper installation was attempted. Global batuta remains beta.21; the previously checksum-verified workspace beta.23 binary is available for orchestration. No background executor remains from this discovery.

## Native macOS investigation outcome

Native macOS remains required. Current host 26.6.2 lacks the macOS 27 descendants-only Endpoint Security API (SDK declaration and runtime symbol check agree). Existing Endpoint Security is a candidate requiring an Apple-approved signing entitlement, root helper and TCC authorization; it does not itself prove cleanup until event-loss and race behavior are tested. kqueue and launchd do not meet the stated escaped-descendant requirement. No universally impossible claim is made. The remaining platform prerequisite cannot be replaced with qualification booleans. Product implementation is not started. Research report .batuta/scout/2026-09-17-macos-lifecycle.md records evidence and rejected overclaims.

A possible native architecture keeps the conductor and all executors unprivileged and isolates only descendant observation/identity-bound cleanup in a signed helper. Before implementation, establish whether the project has the required Apple entitlement and accepts this distribution dependency. This is a new architectural dependency outside the previously approved no-daemon design; neither installation nor TCC changes have been made.

## Compozy comparison — correction to proposed dependency

Inspected local Compozy checkout /Volumes/Home/francisross/Projects/compozy/compozy at b233aead6, read-only. Compozy implements a narrower operational contract: ACP cancellation, owned process group creation, TERM/KILL escalation, bounded group-exit checks and recorded process identity checks. Relevant files: internal/acp/client_control.go, internal/subprocess/process_shutdown.go, internal/procutil/process_group_unix.go, internal/toolruntime/interrupt.go. Tests inspect same-group descendants including root-exit and ignored-TERM cases; no arbitrary escaped-group containment was found. Tests were read, not executed.

Endpoint Security is therefore NOT a prerequisite for native macOS ACP itself. The prior helper proposal applied to the stronger arbitrary-descendant containment requirement. Proposed Batuta direction: evaluate adopting Compozy's explicit managed-process-group contract, with uncertainty retained and no automatic replay, and separately scope escaped-descendant guarantees. This is a proposed acceptance-contract change, not user approval to weaken the existing criterion; no implementation or helper installation has occurred.

## Approved contract revision

User explicitly agreed to the Compozy-style operational contract: native macOS managed process groups, bounded verified group shutdown, explicit escaped-descendant limits, visible cleanup failures and no replay of uncertain work. Endpoint Security/helper dependency is withdrawn. This supersedes arbitrary-descendant containment as a release prerequisite; it does not authorize claiming that escaped descendants are contained. Lifecycle implementation begins on feat/acp-production via codex/gpt-6-astra high.
