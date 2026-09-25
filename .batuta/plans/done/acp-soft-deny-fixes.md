# Plan — acp-soft-deny review fixes: denial records stay small and never carry secrets
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the three blockers of the supervision review of delivery `acp-soft-deny-20260925-142341` (verdict REWORK): the recorded denials can exceed the 4 KiB compact receipt (`ReceiptLimit`), and the denied command, title and locations are copied from the worker without redaction into a receipt that the journal keeps.
**Created:** 2026-09-25 · **Status:** done

## Tasks
- [x] 1. Denial records are redacted and bounded so the receipt always fits — backend/high
      Scope: executor/acp/permission.go, executor/acp/permission_test.go, executor/acp/session.go, executor/acp/session_test.go, executor/receipt.go, executor/receipt_test.go, executor/acp_backend.go, executor/acp_backend_test.go
      Accept: a session keeps at most 4 DeniedPermission records and counts every denial in TurnResult.DeniedPermissionsTotal; each record keeps Title up to 80 bytes, Command up to 160 bytes and at most 2 Locations of up to 160 bytes, cut on a UTF-8 boundary → go test ./executor/acp -run TestSessionRecordsDeniedPermissions; before a field is stored, anything shaped like a secret is replaced by `[redacted]`: the value of a `KEY=value` or `--key value`/`--key=value` pair whose key contains token, secret, password, passwd, api_key, apikey, auth or credential (case-insensitive), `Bearer <value>`, and values starting with `sk-`, `ghp_`, `gho_`, `github_pat_`, `xox` or `AKIA` → go test ./executor/acp -run TestDeniedPermissionRedactsSecrets; the receipt carries `denied_permissions` and `denied_permissions_total`, and MarshalReceipt of a receipt with the maximum denial records at maximum field sizes plus a full Worker.Detail stays within ReceiptLimit → go test ./executor -run TestReceiptWithMaximumDenialsFitsLimit; the packages stay green → go test ./executor ./executor/acp

## Decisions and context

Go standard library only, conventional commits. Behaviour of the denial itself (answer with `reject_once`, continue) does not change. The limits are chosen so four maximal records (~2.7 KiB of JSON) plus the rest of a receipt fit in 4 KiB before MarshalReceipt drops Worker.Detail; if a receipt still overflows, the existing compaction path applies. The loop package has `dropSecretLines`/`redactText` (`loop/judgment.go` ~515, ~666) but executor/acp cannot import loop; write the small redactor in `executor/acp/permission.go` beside `recordDenial` (~107). Sandbox note for the executor: `/bin/ps`, loopback listeners and scratch directories outside the worktree are blocked; run the tests named above, and let the conductor's gate run `go test ./...`.
