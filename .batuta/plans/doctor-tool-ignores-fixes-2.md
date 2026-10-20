# Plan — fix the second review of the doctor tool ignores
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Fix the 4 findings of the automatic review of delivery `doctor-tool-ignores-fixes-20261020-151517` (review `27235ead550d`, verdict REWORK): computed keys holding a comma, TypeScript methods with a return type, the unicode escape test that uses no escape, and the Biome rule sentence of the done plan.
**Created:** 2026-10-20 · **Status:** approved

## Tasks
- [ ] 1. Computed keys with commas, typed method definitions, the unicode escape test and the Biome rule sentence — backend/medium
      Scope: ignores/lexer.go, ignores/lexer_test.go, .batuta/plans/done/doctor-tool-ignores.md
      Accept: `keyLiterals` counts a computed member whose expression holds a top-level comma, such as `[0, 'files']: x`, as the sibling key `[computed]`, and the existing computed cases keep passing → go test -run '^TestKeyLiteralsComputedSibling$' ./ignores/; `callLiterals` skips a TypeScript method definition with a return type, such as `class A { globalIgnores(a = '.batuta'): string[] { return [] } }` and `x = { async globalIgnores(): Promise<string[]> { return [] } }`, and still reads the call in `x = c ? globalIgnores(['a']) : {}` → go test -run '^TestCallLiteralsSkipsDeclarations$' ./ignores/; the "unicode four digits" case of `TestStringLiteralEscapes` feeds the six characters backslash, `u`, `0`, `0`, `2`, `e` before `batuta` and expects `.batuta` → go test -run '^TestStringLiteralEscapes$' ./ignores/ && grep -q 'u002ebatuta' ignores/lexer_test.go; the done plan's Biome v2 sentence states the implemented rule and names `src/.batuta` as a pattern that does not match → grep -q 'src/.batuta' .batuta/plans/done/doctor-tool-ignores.md; the package tests pass → go test ./ignores/ ./cmd/batuta/

## Decisions and context

Source: the review report `.batuta/reviews/supervision/27235ead550dcf5b9c5f39d7f762816757c2a265fd6605e8bddeb89f79731517/artifacts/review.md`. The conductor accepted all 4 findings. Every other behaviour of `ignores/` stays as it is, and every existing test keeps passing. Add the new cases to the named existing tests.

**Task 1.** Computed keys: in `keyLiterals`, find the end of a computed member with a scan for the matching `]` that tracks bracket depth and does not stop at commas, instead of `valueEnd`. Leave `valueEnd` unchanged for its other callers.

Typed methods: in `callLiterals`, a name followed by `(` is a method definition, not a call, when its matching `)` is followed by `{`, as today, or by `:` and then type tokens up to a `{` at bracket depth 0, with no `;`, `,`, `=` or `)` at depth 0 before it, and the name starts a member: the token before it is `{`, `}`, `;` or `,`, or one of the modifiers `async`, `static`, `public`, `private`, `protected`, `readonly`, `get` or `set`. A name after `?`, `:`, `=`, `(` or an operator is still a call, so a ternary branch such as `c ? globalIgnores(['a']) : {}` keeps its literals. Treat `<` and `>` in the type tokens as plain tokens.

Unicode test: the input of the "unicode four digits" case is the Go raw string holding `'\u002ebatuta'`, written with a real backslash. The earlier plan lost the escape when it was written.

Biome sentence: in `.batuta/plans/done/doctor-tool-ignores.md`, change only the Biome version paragraph. Replace "or when it holds `.batuta` as a whole segment, so `.batuta-old` and `x.batuta` do not match" with the rule of `biomePatternMatches` in `ignores/biome.go`: the pattern is `**` or `**/*`, its first segment is `.batuta`, or it is `**/.batuta` or starts with `**/.batuta/`. Say that `.batuta-old`, `x.batuta` and `src/.batuta` do not match. Keep the words "whole segment" in the paragraph, because the proof of the earlier plan greps for them.
