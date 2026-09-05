package routing

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const planFixture = `# Plan — Checkout hardening
<!-- inputs: profile.md@sha256:1a2b3c4d5e6f routing.md@sha256:0f0e0d0c0b0a -->

**Goal:** Make checkout survive a payment timeout
without losing the cart.
**Created:** 2026-09-05 · **Status:** approved

## Tasks
- [x] 1. Reproduce the timeout in a test — testing/low → opencode/kimi-k2.5
      Scope: tests/checkout/timeout.test.ts
      Accept: a failing test reproduces the timeout → npm test -- timeout
- [ ] 2. Retry the payment call once — backend/medium → codex/gpt-5.6-terra
      Depends on: 1
      Scope: src/checkout/payment.ts, tests/checkout/timeout.test.ts
      Accept: the reproduction passes → npm test -- timeout; no retry on 4xx → npm test -- payment
      Anything else here is prose the executor may read.
- [ ] 3. Document the retry policy — docs/low
      Accept: README mentions the retry → grep -n retry README.md

## Decisions and context
Free prose for a fresh session.
`

func TestParsePlanReadsTheMachineContract(t *testing.T) {
	t.Parallel()
	plan, err := ParsePlan("checkout-hardening", []byte(planFixture))
	if err != nil {
		t.Fatalf("ParsePlan() error = %v", err)
	}
	if plan.Title != "Checkout hardening" || plan.Status != PlanApproved || plan.Created != "2026-09-05" ||
		plan.Goal != "Make checkout survive a payment timeout without losing the cart." {
		t.Fatalf("header = %#v", plan)
	}
	if len(plan.Tasks) != 3 || len(plan.Set.Tasks) != 3 || plan.Set.Digest == "" {
		t.Fatalf("tasks = %d, set = %d, digest %q", len(plan.Tasks), len(plan.Set.Tasks), plan.Set.Digest)
	}
	first, second, third := plan.Tasks[0], plan.Tasks[1], plan.Tasks[2]
	if first.ID != "task_1" || first.Status != "completed" || first.Domain != DomainTesting || first.Complexity != ComplexityLow ||
		first.Executor != "opencode" || first.Model != "kimi-k2.5" || len(first.Scope) != 1 || len(first.Accept) != 1 {
		t.Fatalf("task 1 = %#v", first)
	}
	if second.ID != "task_2" || second.Status != "pending" || second.Executor != "codex" || second.Model != "gpt-5.6-terra" ||
		len(second.Dependencies) != 1 || second.Dependencies[0] != "task_1" || len(second.Scope) != 2 || len(second.Accept) != 2 ||
		!strings.Contains(string(second.Content), "prose the executor may read") {
		t.Fatalf("task 2 = %#v", second)
	}
	if third.Executor != "" || third.Domain != DomainDocs || len(third.Dependencies) != 0 {
		t.Fatalf("task 3 = %#v", third)
	}
	if first.Digest == second.Digest || len(first.Digest) != 64 {
		t.Fatalf("digests = %q %q", first.Digest, second.Digest)
	}
	snapshot, err := plan.Set.DeliverySnapshot()
	if err != nil || len(snapshot.IncompleteTaskIDs) != 2 || snapshot.Digest != plan.Set.Digest {
		t.Fatalf("DeliverySnapshot() = %#v, %v", snapshot, err)
	}
}

func TestParsePlanRejectsBrokenContractsWithTheLine(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		edit func(string) string
		want string
	}{
		"missing accept": {func(s string) string {
			return strings.Replace(s, "      Accept: README mentions the retry → grep -n retry README.md\n", "", 1)
		}, "task 3 has no Accept"},
		"unknown lane": {func(s string) string { return strings.Replace(s, "backend/medium", "backend/huge", 1) }, "line 12: unknown lane"},
		"dependency cycle": {func(s string) string {
			s = strings.Replace(s, "Depends on: 1", "Depends on: 3", 1)
			return strings.Replace(s, "      Accept: README mentions the retry → grep -n retry README.md\n", "      Depends on: 2\n      Accept: README mentions the retry → grep -n retry README.md\n", 1)
		}, "cycle"},
		"missing dependency": {func(s string) string { return strings.Replace(s, "Depends on: 1", "Depends on: 9", 1) }, "does not exist"},
		"status in prose only": {func(s string) string {
			return strings.Replace(s, "**Created:** 2026-09-05 · **Status:** approved", "Created 2026-09-05, **Status:** approved in prose", 1)
		}, "no `**Status:**`"},
		"status suffix": {func(s string) string {
			return strings.Replace(s, "**Status:** approved", "**Status:** approved-pending-review", 1)
		}, "exactly proposed"},
		"status twice": {func(s string) string { return strings.Replace(s, "## Tasks", "**Status:** done\n\n## Tasks", 1) }, "already declared"},
		"status twice one line": {func(s string) string {
			return strings.Replace(s, "**Status:** approved", "**Status:** approved · **Status:** done", 1)
		}, "once"},
		"duplicate number": {func(s string) string { return strings.Replace(s, "- [ ] 3.", "- [ ] 2.", 1) }, "already defined"},
		"malformed task line": {func(s string) string {
			return strings.Replace(s, "- [ ] 3. Document the retry policy — docs/low", "- [ ] 3. Document the retry policy", 1)
		}, "task line must read"},
		"no status":               {func(s string) string { return strings.Replace(s, "**Status:** approved", "**State:** approved", 1) }, "no `**Status:**`"},
		"no title":                {func(s string) string { return strings.Replace(s, "# Plan — Checkout hardening", "# Checkout", 1) }, "line 1"},
		"no tasks":                {func(s string) string { return s[:strings.Index(s, "## Tasks")] + "## Tasks\n" }, "no tasks"},
		"dependency not a number": {func(s string) string { return strings.Replace(s, "Depends on: 1", "Depends on: task_1", 1) }, "lists other tasks"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePlan("checkout-hardening", []byte(tc.edit(planFixture)))
			if !errors.Is(err, ErrReauthoringRequired) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want ErrReauthoringRequired containing %q", err, tc.want)
			}
		})
	}
	if _, err := ParsePlan("Bad Slug", []byte(planFixture)); !errors.Is(err, ErrInvalidSlug) {
		t.Fatalf("bad slug error = %v", err)
	}
}

func TestPlanLoaderReadsFromBatutaDirectoryOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".batuta", "plan-checkout-hardening.md"), []byte(planFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".batuta", "plan-Not A Slug.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	loader, err := NewPlanLoader(root)
	if err != nil {
		t.Fatalf("NewPlanLoader() error = %v", err)
	}
	var source TaskSource = loader
	set, err := source.Load("checkout-hardening")
	if err != nil || set.Slug != "checkout-hardening" || len(set.Tasks) != 3 {
		t.Fatalf("Load() = %#v, %v", set, err)
	}
	if _, err := loader.Load("missing-plan"); err == nil {
		t.Fatal("Load(missing) should fail")
	}
	slugs, err := loader.ListPlans()
	if err != nil || len(slugs) != 1 || slugs[0] != "checkout-hardening" {
		t.Fatalf("ListPlans() = %v, %v", slugs, err)
	}
}

func TestParsePlanIgnoresStatusInCommentsAndFencesAndOrdersForwardDependencies(t *testing.T) {
	t.Parallel()
	payload := strings.Replace(planFixture, "**Created:** 2026-09-05 · **Status:** approved",
		"<!-- example: **Status:** approved -->\n<!--\n**Status:** approved\n-->\n```\n**Status:** approved\n```\n~~~\n**Status:** approved\n~~~\n**Created:** 2026-09-05 · **Status:** proposed", 1)
	payload = strings.Replace(payload, "Depends on: 1", "Depends on: 3", 1)
	plan, err := ParsePlan("checkout-hardening", []byte(payload))
	if err != nil {
		t.Fatalf("ParsePlan() error = %v", err)
	}
	if plan.Status != PlanProposed {
		t.Fatalf("status = %q, want proposed (comment and fence ignored)", plan.Status)
	}
	if plan.Tasks[1].ID != "task_2" || plan.Tasks[1].Dependencies[0] != "task_3" {
		t.Fatalf("file order must be kept in plan.Tasks: %#v", plan.Tasks[1])
	}
	ids := []string{plan.Set.Tasks[0].ID, plan.Set.Tasks[1].ID, plan.Set.Tasks[2].ID}
	if ids[0] != "task_1" || ids[1] != "task_3" || ids[2] != "task_2" {
		t.Fatalf("set order = %v, want dependencies first", ids)
	}
	if _, err := plan.Set.DeliverySnapshot(); err != nil {
		t.Fatalf("DeliverySnapshot() on the ordered set error = %v", err)
	}
}
