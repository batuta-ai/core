# Plan — loop briefs carry the profile's own Conventions
<!-- inputs: profile.md@sha256:e18a00765937 routing.md@sha256:bdb31fda5c7d -->

**Goal:** Close core#161: every executor brief the loop writes carries the body of `.batuta/profile.md`'s `## Conventions` section first, then the stack template sections, the way the interactive cycle's brief does ("Conventions = profile + stack template + its Extends chain"). Plans stop repeating the profile's rules in their Decisions.
**Created:** 2026-10-10 · **Status:** done

## Tasks
- [x] 1. Brief writes the profile's Conventions section before the template sections — go/medium
      Scope: loop/brief.go, loop/brief_test.go
      Accept: a brief built from a profile whose raw text has a `## Conventions` section carries that body under `### From .batuta/profile.md`, after the Stack/Methodology/Test lines and before the first template section → go test -run TestBriefCarriesProfileConventions ./loop/; a profile without the section yields exactly today's `## Conventions` section text, and the `Unknown — no stack template was found` line appears only when both the profile section and the template sections are empty → go test -run TestBriefWithoutProfileConventions ./loop/; the whole suite passes → go test ./...

## Decisions and context
The profile parser already exposes the section: `Profile.ConventionsSection()` in `loop/profile.go` returns the trimmed body of the exact `## Conventions` heading (not `## Conventions for briefs`), or "" when absent. `Brief` already receives the whole `Profile` in `BriefInput.Profile`, so no caller changes: read the section inside `Brief`, in `loop/brief.go` under `## Conventions`. Do not change `BriefInput`, `ConventionsSection` or `loop/attempt.go`.

Use the same subheading `batuta council` uses in `cmd/batuta/council.go` (`"### From .batuta/profile.md\n\n" + body`), followed by a blank line, so both readers see the same shape. Order inside `## Conventions`: the Stack, Methodology and Test command lines, the "Leave no TODO" line, the profile section, then the `Unknown — no stack template was found` line only when no template section exists and the profile section is empty, then each template section, then the missing-templates note.

Tests are table-driven with `t.Parallel()` (profile Conventions). Build the profile with `ParseProfile(raw)` so `Raw` is set; assert order with `strings.Index` comparisons, not only `strings.Contains`. `TestBriefWithoutProfileConventions` cuts the brief's `## Conventions` section (up to the next `\n## `) and compares it with a string literal written from today's output, so it fails if the new code adds a heading or blank line unconditionally; its table covers no profile section with templates, no profile section without templates (Unknown line present), and a profile section without templates (Unknown line absent).

The skills checks (`tests/skills/check.sh` in batuta-ai/skills) budget brief size by template sections; a long profile section now adds to every loop brief. That repository is out of scope here; note it in the PR body.
