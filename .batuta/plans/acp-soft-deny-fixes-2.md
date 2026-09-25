# Plan — acp-soft-deny review fixes 2: a secret-shaped key redacts the rest of the field
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the blocker of the supervision review of delivery `acp-soft-deny-fixes-20260925-150706` (verdict REWORK): `redactDenialField` stops a secret value at a closing quote, so `TOKEN="private"suffix` keeps `suffix`. Shell words can join quoted and unquoted segments and escaped spaces in many ways; instead of parsing them, redact conservatively.
**Created:** 2026-09-25 · **Status:** approved

## Tasks
- [ ] 1. Once a secret-shaped key or prefix appears, everything after it in the field is redacted — backend/medium
      Scope: executor/acp/permission.go, executor/acp/permission_test.go
      Accept: for a `KEY=value`, `--key value` or `--key=value` pair whose key is secret-shaped, and for `Bearer ` and the `sk-`, `ghp_`, `gho_`, `github_pat_`, `xox`, `AKIA` prefixes, the field keeps the text up to and including the key (or up to the prefix) and replaces everything from there to the end of the field with `[redacted]` → go test ./executor/acp -run TestDeniedPermissionRedactsSecrets; the regression table includes `TOKEN="private"suffix`, `TOKEN='a b'c`, `TOKEN=a\ b`, `--api-key "x"y`, `curl -H "Authorization: Bearer abc" url` and a command with no secret, which stays unchanged → go test ./executor/acp -run TestDeniedPermissionRedactsSecrets; the package stays green → go test ./executor/acp ./executor

## Decisions and context

Go standard library only, conventional commits. Losing the tail of a denied command is acceptable: the record says what kind of action was denied, not how to replay it. The redactor is `redactDenialField` in `executor/acp/permission.go` (~31) with the patterns near line 15. Sandbox note: run the named tests; the conductor's gate runs `go test ./...`.
