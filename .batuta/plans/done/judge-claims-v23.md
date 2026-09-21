# Plan — claim_evidence v2.3: path-claim recall from real executor reports
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:1615c7990def -->

**Goal:** Restore the two true positives the v2.2 extractor lost without giving back the false positives: read touched-file lists the way real executor reports write them (heading in either word order, singular or plural, items as Markdown links) and accept a few more edit verbs, keeping the v2.2 precision tests green.
**Created:** 2026-09-21 · **Status:** done

## Tasks
- [x] 1. Touched-file headings in either order and Markdown-link items — backend/medium
      Scope: loop/claims.go, loop/claims_test.go
      Accept: headings Paths touched, Touched path, Touched paths, Files changed, Changed files, Modified files, Edited files, with or without a leading #, colon or trailing text, start a list of path claims → go test ./loop -run TestExtractClaimsHeadingOrders; a list item written as a Markdown link yields the path from the link text with backticks stripped, or from a file:// target when the text is not a path, made repository-relative when the target contains /.batuta/worktrees/<name>/ → go test ./loop -run TestExtractClaimsMarkdownLinkItems; the two real report excerpts quoted in .batuta/judge-benchmark.md "Version 2.2 replay" each produce exactly one path claim .batuta/routing.md → go test ./loop -run TestExtractClaimsBenchmarkRecall; the edit-verb list gains refreshed, reseated, replaced, patched, reworked, adjusted, touched, changed and the v2.2 rejection table still yields zero path claims → go test ./loop -run 'TestExtractClaimsRejectsMentions|TestExtractClaimsBenchmarkFalsePositives|TestExtractClaimsPathPrecision'; the package stays green → go test ./loop

## Decisions and context

Go standard library only, frozen exported signatures, conventional commits, no judge calls in tests. Read the "Version 2.2 replay" section of `.batuta/judge-benchmark.md` first; it quotes the two report shapes that must produce a claim. The `known` gate from v2.2 stays: a path still has to exist in the tree, in changed_paths or match the Scope, which is what keeps precision. After this task the conducting host reruns the replay and records "Version 2.3 replay".

**Task 1.** In `loop/claims.go` replace `claimFilesHeading` with a pattern that accepts `(?i)^(?:#{1,6}\s+)?(?:(?:paths?|files?)\s+(?:touched|changed|modified|edited)|(?:touched|changed|modified|edited)\s+(?:paths?|files?))\b\s*:?\s*(.*)$`. Before token extraction on a list item, rewrite a Markdown link `[text](target)`: if `text` (backticks stripped) qualifies as a path, use it; else if `target` starts with `file://`, take the part after `/.batuta/worktrees/<name>/` when present, else the path relative to the workspace root; drop the link otherwise. Extend `claimEditVerb` with the verbs in the acceptance criterion. Add the two excerpts as table tests exactly as written in the benchmark (heading line plus link item) with `known` accepting `.batuta/routing.md`.
