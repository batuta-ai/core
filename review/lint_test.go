package review

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/publication"
)

func TestLintOverlapSuppression(t *testing.T) {
	manifest, runtime, opts := sessionFixture(t, 1)
	writeTestFile(t, opts.Root, ".batuta/profile.md", "Stack: Go\nTemplate: templates/go.md\nLint: project-lint --unix\n")
	order := make(chan string, 2)
	opts.LintRunner = reviewCommandRunner(func(_ context.Context, cmd publication.Command) (publication.CommandResult, error) {
		order <- "lint"
		if cmd.Executable != "/bin/sh" || !strings.Contains(strings.Join(cmd.Args, " "), "project-lint --unix") {
			t.Errorf("lint invocation=%+v", cmd)
		}
		return publication.CommandResult{ExitCode: 1, Stdout: []byte("file0.go:1:7: changed invariant [lint/rule]\nfile0.go:9: unchanged line\nother.go:1: other file\n")}, nil
	})
	useReviewRunner(&opts, func(_ context.Context, _ publication.Command) (publication.CommandResult, error) {
		order <- "review"
		return publication.CommandResult{Stdout: []byte("<<<FINDINGS\n" +
			`{"severity":"major","kind":"defect","file":"file0.go","line":1,"premise":"changed invariant","path":"call path","verdict":"wrong output","rule":"different reviewer label"}` + "\n" +
			"FINDINGS>>>\n")}, nil
	})

	results, err := RunCohorts(t.Context(), manifest, runtime, opts)
	if err != nil {
		t.Fatal(err)
	}
	if first, second := <-order, <-order; first != "lint" || second != "review" {
		t.Fatalf("order=%q, %q", first, second)
	}
	if len(results) != 1 || !results[0].Covered || len(results[0].Findings) != 0 || len(results[0].Suppressed) != 1 {
		t.Fatalf("results=%+v", results)
	}
	if got := results[0].Suppressed[0]; got.Linter.File != "file0.go" || got.Linter.Line != 1 || got.Linter.Rule != "changed invariant [lint/rule]" {
		t.Fatalf("suppressed=%+v", got)
	}
}

func TestParseLintDiagnosticsChangedLinesOnly(t *testing.T) {
	root := t.TempDir()
	manifest := Manifest{Files: []File{{Path: "src/a.go", Selected: true, Hunks: []Hunk{{Start: 10, Count: 2}}}}}
	output := strings.Join([]string{
		"src/a.go:10: message one",
		"src/a.go:11:4: message two",
		filepath.Join(root, "src/a.go") + ":10: absolute path",
		"src/a.go:12: unchanged",
		"src/b.go:10: unselected",
		"noise",
	}, "\n")
	got := ParseLintDiagnostics(root, output, manifest)
	if len(got) != 3 || got[0].Line != 10 || got[0].Rule != "message one" || got[1].Line != 11 || got[2].File != "src/a.go" {
		t.Fatalf("diagnostics=%+v", got)
	}
}

func TestProfileLintCommand(t *testing.T) {
	payload := "Lint: golangci-lint run\nLint: ignored\n"
	if got := ProfileLintCommand(payload); got != "golangci-lint run" {
		t.Fatalf("command=%q", got)
	}
	if got := ProfileLintCommand("Lint:\n"); got != "" {
		t.Fatalf("empty command=%q", got)
	}
}
