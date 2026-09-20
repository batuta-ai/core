# Plan — read cursor-agent's model list through its ANSI colors
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Make the cursor-agent inventory adapter parse the model list the CLI actually prints, which wraps every slug and description in ANSI escape sequences even when piped, so cursor rows in a routing table stop being rejected as unlisted models.
**Created:** 2026-09-20 · **Status:** done

## Tasks
- [x] 1. Strip ANSI escapes before parsing cursor-agent model lines — backend/medium
      Scope: inventory/adapters/cursor.go, inventory/adapters/adapters_test.go, inventory/adapters/testdata/cursor.json, inventory/adapters/testdata/cursor-ansi.json
      Accept: a models probe output whose lines carry ANSI SGR sequences yields the plain slugs as cursor bindings → go test ./inventory/adapters -run TestCursorModelsParseThroughAnsi; the existing plain fixture keeps yielding the same bindings and evidence state → go test ./inventory/adapters -run 'TestCursor|TestModelBindingsBackDoctorCountsForEveryExecutor'; the models evidence is resolved and its raw payload is kept verbatim → go test ./inventory/adapters -run TestCursorModelsParseThroughAnsi; the package and the module stay green → go build ./... && go test ./inventory/... ./routing/...

## Decisions and context

Go standard library only, frozen exported signatures, conventional commits. Evidence on this machine on 2026-09-20: `cursor-agent --list-models` and `cursor-agent models` print lines such as `ESC[36mcursor-grok-4.6-highESC[39m ESC[2m- Cursor Grok 4.6ESC[22m` (ESC = 0x1b) even when stdout is a pipe and with `NO_COLOR=1` or `TERM=dumb`; `batuta inventory` therefore reports `models: unknown` for cursor-agent and only the bare `cursor` provider binding, and `routing.RoutingTable.Generation` rejects every cursor row as an unlisted model.

**Task 1.** In `normalizeCursor` (`inventory/adapters/cursor.go`), strip CSI sequences (`ESC [ ... final byte in 0x40–0x7E`) from each line before splitting on ` - `; keep `safePublicIdentifier` as the gate on the slug and keep the raw probe output untouched in the evidence payload. Implement the stripper as a small unexported helper in the adapters package usable by other adapters; a regexp from the standard library is fine. Add a fixture `inventory/adapters/testdata/cursor.json, inventory/adapters/testdata/cursor-ansi.json` captured from the real CLI (a handful of lines is enough, including the `auto` line with its `(default)` suffix and one Grok slug) and a test asserting the parsed bindings include `auto` and `cursor-grok-4.6-high`.
