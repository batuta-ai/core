# Plan — acp-worktree-permissions review fixes: an unresolvable symlink or a `..` never passes as inside the worktree
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Close the code blocker of the review of delivery `acp-worktree-permissions-20260923-142155` (verdict REWORK, `.batuta/reviews/2026-09-23-acp-worktree-permissions/`) on branch `feat/acp-bridge-qualification`: `resolveWorktreeLocation` treats every `filepath.EvalSymlinks` error as a missing path, so a dangling symlink inside the worktree that points outside is skipped and the request is allowed. The probe-log findings of the same review were fixed by the conductor in commit `f4a95aa`.
**Created:** 2026-09-23 · **Status:** done

## Tasks
- [x] 1. The worktree policy skips only components that do not exist and rejects `..` and unresolvable links — backend/high
      Scope: executor/permission_policy.go, executor/permission_policy_test.go
      Accept: a location under a dangling symlink inside cwd whose target is outside cwd is rejected → go test ./executor -run 'TestWorktreePolicyRejects/dangling_symlink_outside'; a location under a dangling symlink inside cwd whose target would be inside cwd is also rejected, since the link cannot be resolved → go test ./executor -run 'TestWorktreePolicyRejects/dangling_symlink_inside'; a location whose raw path contains a `..` component is rejected before any cleaning → go test ./executor -run 'TestWorktreePolicyRejects/dot_dot_component'; a location whose missing tail lies under an existing directory inside cwd is still allowed → go test ./executor -run TestWorktreePolicyAllowsInsideLocations; the package stays green → go test ./executor

## Decisions and context

Go standard library only, conventional commits. Nothing else changes.

**Task 1.** In `resolveWorktreeLocation` (`executor/permission_policy.go` ~36), walk up only while `os.Lstat(current)` reports `os.ErrNotExist` (`errors.Is`); when `Lstat` succeeds but `filepath.EvalSymlinks` fails (a dangling or looping link) or `Lstat` fails with any other error, return `"", false`. Reject a raw path that has a `..` element (split on the separator before `filepath.Clean`), because lexical cleaning of `/cwd/link/../x` hides where the kernel would go. Build the dangling-link fixtures with `os.Symlink` to a path that does not exist, under the symlink-resolved temp dir the existing tests use; add the new cases as named subtests of the existing `TestWorktreePolicyRejects` table.
