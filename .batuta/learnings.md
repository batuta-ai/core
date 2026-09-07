
## 2026-09-06 — loop runs on core (roadmap, dashboard)

- A docs task's criteria must protect the existing content: `grep -c '^## ' docs/loop.md` not below its previous count, or a named section still present. Task 5 of `roadmap` (gpt-5.4-mini) replaced `docs/loop.md` with a ten-line stub and passed a proof that only grepped one heading.
- Never join two behaviours with different outcomes in one criterion ("a blocked or waiting_input delivery … and --resume continues"): the read-only verifier reads it literally and vetoes the honest implementation. One criterion per outcome.
- Sandbox recipe for Go on this machine (codex `--sandbox workspace-write`): `HOME=/private/tmp/batuta-home GOCACHE=/private/tmp/batuta-gocache GOPATH=/Volumes/Home/francisross/go GOMODCACHE=/Volumes/Home/francisross/go/pkg/mod`; `GOTOOLCHAIN` stays `auto` (go1.26.4 is cached in the module cache; the PATH go is 1.24.2). Write it in the plan's shared Decisions with "environment setup is never a question" — gpt-6-astra otherwise spends executions asking (dashboard task 2: three questions, delivery abandoned).
