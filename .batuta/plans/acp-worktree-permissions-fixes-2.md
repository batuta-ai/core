# Plan — acp-worktree-permissions review fixes 2: `..` is found with every path separator the platform accepts
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Close the code finding of the second review of branch `feat/acp-bridge-qualification` (verdict REWORK, `.batuta/reviews/2026-09-23-acp-worktree-permissions-fixes/`): `resolveWorktreeLocation` splits the raw path only on `filepath.Separator`, so on Windows a `..` written with `/` survives the check and `filepath.Clean` then collapses it. The probe and log findings of the same review were fixed by the conductor in commit `13320fd`.
**Created:** 2026-09-23 · **Status:** approved

## Tasks
- [ ] 1. The worktree policy rejects a `..` element split on every platform path separator — backend/medium
      Scope: executor/permission_policy.go, executor/permission_policy_test.go
      Accept: the raw location is split with os.IsPathSeparator, so on every platform any `..` element between separators the platform accepts is rejected before filepath.Clean → go test ./executor -run 'TestWorktreePolicyRejects/dot_dot_component'; a helper test feeds the splitting function a path with `..` between `/` separators and one between filepath.Separator separators and expects both found → go test ./executor -run TestWorktreeLocationHasDotDot; the package stays green → go test ./executor

## Decisions and context

Go standard library only, conventional commits. `cmd/batuta` builds on windows (profile), so the fix must compile everywhere with no build tags. Replace the `strings.Split(path, string(filepath.Separator))` loop in `resolveWorktreeLocation` (`executor/permission_policy.go` ~39) with a small helper that walks the string and splits where `os.IsPathSeparator` is true; `/` is a separator on every platform Go supports, `\` only on Windows.
