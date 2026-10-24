package routing

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTaskGrantValidation(t *testing.T) {
	t.Parallel()

	paths := func(count int) []string {
		out := make([]string, count)
		for index := range out {
			out[index] = fmt.Sprintf("src/p%d.go", index)
		}
		return out
	}
	operations := func(count int) []string {
		out := make([]string, count)
		for index := range out {
			out[index] = fmt.Sprintf("op-%d", index)
		}
		return out
	}
	tests := []struct {
		name  string
		grant TaskGrant
		valid bool
	}{
		{name: "empty grant", grant: TaskGrant{}, valid: true},
		{name: "paths and operations", grant: TaskGrant{Paths: []string{"src/**", "cmd/app/main.go"}, Operations: []string{"edit", "run-tests"}}, valid: true},
		{name: "until task", grant: TaskGrant{Paths: []string{"src/**"}, Until: "task"}, valid: true},
		{name: "unknown until", grant: TaskGrant{Paths: []string{"src/**"}, Until: "delivery"}},
		{name: "absolute path", grant: TaskGrant{Paths: []string{"/etc/passwd"}}},
		{name: "parent segment", grant: TaskGrant{Paths: []string{"../outside"}}},
		{name: "nested parent segment", grant: TaskGrant{Paths: []string{"src/../../outside"}}},
		{name: "bare parent", grant: TaskGrant{Paths: []string{".."}}},
		{name: "invalid glob", grant: TaskGrant{Paths: []string{"src/[a-"}}},
		{name: "empty path", grant: TaskGrant{Paths: []string{""}}},
		{name: "unclean path", grant: TaskGrant{Paths: []string{"./src"}}},
		{name: "backslash path", grant: TaskGrant{Paths: []string{`src\main.go`}}},
		{name: "control byte path", grant: TaskGrant{Paths: []string{"src/a\nb"}}},
		{name: "path at byte limit", grant: TaskGrant{Paths: []string{strings.Repeat("a", maxGrantPathBytes)}}, valid: true},
		{name: "path over byte limit", grant: TaskGrant{Paths: []string{strings.Repeat("a", maxGrantPathBytes+1)}}},
		{name: "paths at limit", grant: TaskGrant{Paths: paths(maxGrantPaths)}, valid: true},
		{name: "paths over limit", grant: TaskGrant{Paths: paths(maxGrantPaths + 1)}},
		{name: "operations at limit", grant: TaskGrant{Operations: operations(maxGrantOperations)}, valid: true},
		{name: "operations over limit", grant: TaskGrant{Operations: operations(maxGrantOperations + 1)}},
		{name: "operation uppercase", grant: TaskGrant{Operations: []string{"Edit"}}},
		{name: "operation leading digit", grant: TaskGrant{Operations: []string{"1edit"}}},
		{name: "operation with space", grant: TaskGrant{Operations: []string{"run tests"}}},
		{name: "operation empty", grant: TaskGrant{Operations: []string{""}}},
		{name: "operation too long", grant: TaskGrant{Operations: []string{"a" + strings.Repeat("b", 32)}}},
		{name: "operation at length limit", grant: TaskGrant{Operations: []string{"a" + strings.Repeat("b", 31)}}, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.grant.valid(); got != test.valid {
				t.Fatalf("valid(%#v) = %v, want %v", test.grant, got, test.valid)
			}
		})
	}
}

func TestTaskGrantNormalize(t *testing.T) {
	t.Parallel()

	got := TaskGrant{Paths: []string{"b/**", "a/**", "b/**"}, Operations: []string{"run", "edit", "run"}}.Normalize()
	want := TaskGrant{Paths: []string{"a/**", "b/**"}, Operations: []string{"edit", "run"}, Until: GrantUntilTask}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize() = %#v, want %#v", got, want)
	}
	if empty := (TaskGrant{Paths: []string{}, Until: "task"}).Normalize(); !reflect.DeepEqual(empty, TaskGrant{}) {
		t.Fatalf("Normalize(empty) = %#v, want zero value", empty)
	}
}

func TestGrantBoundaryPaths(t *testing.T) {
	t.Parallel()

	boundary := []string{
		"WORK.md", ".batuta/plan.md", ".batuta/worktrees/x/y.go",
		".github/workflows/ci.yml", ".gitlab-ci.yml", ".circleci/config.yml", "azure-pipelines.yml",
		".buildkite/pipeline.yml", "Jenkinsfile",
		"LICENSE", "LICENSE.md", "pkg/LICENSE", "COPYING", "a/b/COPYING.txt", "NOTICE", "sub/NOTICE.md", "docs/NOTICES",
		"go.sum", "sub/go.sum", "package-lock.json", "web/package-lock.json", "npm-shrinkwrap.json",
		"yarn.lock", "pnpm-lock.yaml", "bun.lockb", "bun.lock", "Cargo.lock", "x/Cargo.lock",
		"poetry.lock", "uv.lock", "Pipfile.lock", "Gemfile.lock", "composer.lock", "deep/er/composer.lock",
	}
	for _, target := range boundary {
		if !IsGrantBoundary(target) {
			t.Errorf("IsGrantBoundary(%q) = false, want true", target)
		}
	}
	outside := []string{
		"src/main.go", "docs/WORK.md", "sub/.batuta/x", "sub/.github/workflows/ci.yml", "sub/.gitlab-ci.yml",
		"sub/Jenkinsfile", "LICENSES/other.go", "go.sum.go", "go.mod", "package.json", "x.lock",
		"not-Cargo.lock.rs",
	}
	for _, target := range outside {
		if IsGrantBoundary(target) {
			t.Errorf("IsGrantBoundary(%q) = true, want false", target)
		}
	}

	rejected := []string{"WORK.md", ".github/workflows/ci.yml", "sub/go.sum", "pkg/LICENSE", ".batuta/x", ".batuta"}
	for _, grantPath := range rejected {
		if (TaskGrant{Paths: []string{grantPath}}).valid() {
			t.Errorf("grant path %q accepted, want rejected", grantPath)
		}
	}
	for _, grantPath := range []string{"**", "src/**", "docs/*.md", "pkg/**/*.go"} {
		if !(TaskGrant{Paths: []string{grantPath}}).valid() {
			t.Errorf("grant path %q rejected, want accepted", grantPath)
		}
	}
}
