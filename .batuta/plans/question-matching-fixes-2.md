# Plan — question-matching review fixes 2: the output guard compares against the selected journal files, the UTF-8 test crosses the bound
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close the two findings of the supervision review of delivery `question-matching-fixes-20261001-182546` (verdict REWORK): `build` guards `--out` by the resolved target's suffix and directory instead of comparing it with the journal files it will read, so a journal symlink to a file elsewhere escapes the guard; and the two-byte case of the UTF-8 bound test places its first multibyte character after the 4000-byte limit instead of across it. Nothing in `.batuta/judge-research.md` section 15 changes.
**Created:** 2026-10-01 · **Status:** approved

## Tasks
- [ ] 1. build compares --out with every selected journal file; the UTF-8 bound test crosses the limit — backend/medium
      Scope: cmd/batuta/judge_questions.go, cmd/batuta/judge_questions_test.go, questions/passage_test.go
      Accept: `judge questions build` collects the journal files it will read first, resolves each one and `--out` with `filepath.Abs` and `filepath.EvalSymlinks`, and exits 1 before creating or truncating anything when the resolved `--out` equals any resolved journal file, whatever the target's name, suffix or directory → go test ./cmd/batuta -run TestQuestionsBuildOutIsInput; a journal directory entry named `*.jsonl` that is a symlink to a file without that suffix outside the directory is read as a journal, and an `--out` naming that target is refused → go test ./cmd/batuta -run TestQuestionsBuildOutIsSymlinkTarget; an `--out` that does not exist, or exists outside the selected journal files, is created as before → go test ./cmd/batuta -run TestQuestionsBuildOffline; in TestPassageUTF8Bound every multibyte case starts its first multibyte character at a byte before 4000 and ends it at or after byte 4000, so the cut falls inside that character, and the test asserts the cut passage ends before that character → go test ./questions -run TestPassageUTF8Bound; the packages stay green → go test ./cmd/batuta ./questions

## Decisions and context

Go standard library only, conventional commits, table-driven tests with `t.Parallel()`. Keep the task to the Accept lines: no refactor, no renamed field, no change to `questions/passage.go` or `questions/kind.go`, no change to section 15.

Environment setup is never a question. For Go inside a sandbox on this machine: `HOME=/private/tmp/batuta-home GOCACHE=/private/tmp/batuta-gocache GOPATH=/Volumes/Home/francisross/go GOMODCACHE=/Volumes/Home/francisross/go/pkg/mod`, `GOTOOLCHAIN` stays `auto`. Run the tests named in the Accept lines. If the sandbox blocks the Go build cache, write the tests and say so in the report: the conductor's gate runs the whole suite.

**Task 1.** The guard is `questionsRefuseJournalOut` (`cmd/batuta/judge_questions.go` ~130–150) and the journal selection is in `buildQuestionsCorpus` (~96 onward): move the selection of journal files before the guard and compare resolved paths, dropping the suffix and directory tests. The test prefix in `TestPassageUTF8Bound` (`questions/passage_test.go` ~72–90) is `Short title\n\n`, 13 bytes; with 3987 ASCII bytes the first `é` starts at byte 4000. Use counts that put each first multibyte character at byte 3999 or earlier and ending at or after byte 4000, and assert against that character, not only against `utf8.ValidString`.
