package review

import (
	"bytes"
	"context"
	"errors"
	"os"
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
	if len(results) != 1 || !results[0].Covered || len(results[0].Findings) != 1 || len(results[0].Suppressed) != 0 {
		t.Fatalf("results=%+v", results)
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

func TestRunLintPropagatesExecutionFailure(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "not-executable", "exit 0\n")
	for _, tc := range []struct {
		name, command, directory, message string
		code                              int
	}{
		{"missing", "./missing-linter", root, "missing-linter", 127},
		{"not executable", "./not-executable", root, "not-executable", 126},
		{"spawn", "true", filepath.Join(root, "absent"), "", -1},
		{"unsupported", "printf 'lint failed' >&2; exit 2", root, "lint failed", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := RunLint(t.Context(), tc.directory, tc.command, Manifest{}, nil)
			if err == nil || result.ExitCode != tc.code || !strings.Contains(err.Error(), tc.message) || len(result.Diagnostics) != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	for _, code := range []int{126, 127} {
		runner := reviewCommandRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
			return publication.CommandResult{ExitCode: code, Stderr: []byte("execution failed"), Stdout: []byte("a.go:1: rule")}, nil
		})
		result, err := RunLint(t.Context(), root, "lint", Manifest{Files: []File{{Path: "a.go", Selected: true, Hunks: []Hunk{{Start: 1, Count: 1}}}}}, runner)
		if err == nil || !strings.Contains(err.Error(), "execution failed") || len(result.Diagnostics) != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	cause := errors.New("spawn failed")
	runner := reviewCommandRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
		return publication.CommandResult{ExitCode: -1, Stderr: []byte("spawn details")}, cause
	})
	if _, err := RunLint(t.Context(), root, "lint", Manifest{}, runner); !errors.Is(err, cause) || !strings.Contains(err.Error(), "spawn details") {
		t.Fatalf("error=%v", err)
	}
}

func TestRunLintRejectsTruncatedOutput(t *testing.T) {
	manifest := Manifest{Files: []File{{Path: "a.go", Selected: true, Hunks: []Hunk{{Start: 1, Count: 1}}}}}
	for _, tc := range []struct {
		name            string
		stdoutTruncated bool
		stderrTruncated bool
	}{
		{name: "stdout", stdoutTruncated: true},
		{name: "stderr", stderrTruncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := reviewCommandRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
				return publication.CommandResult{
					ExitCode:        0,
					Stdout:          []byte("a.go:1: incomplete diagnostic"),
					StdoutTruncated: tc.stdoutTruncated,
					StderrTruncated: tc.stderrTruncated,
				}, nil
			})
			result, err := RunLint(t.Context(), t.TempDir(), "lint", manifest, runner)
			if err == nil || !strings.Contains(err.Error(), "truncated") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Error != err.Error() || len(result.Diagnostics) != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestReportShowsLintStatus(t *testing.T) {
	for _, tc := range []struct {
		name, command, want string
		fails               bool
	}{
		{"unconfigured", "", "Lint: not configured", false},
		{"clean", "true", "Lint: completed (exit 0)", false},
		{"diagnostics", "printf 'file0.go:1: rule\\n'; exit 1", "Lint: completed (exit 1)", false},
		{"missing", "./missing-linter", "Lint: failed (exit 127)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, runtime, opts := sessionFixture(t, 1)
			writeTestFile(t, opts.Root, ".batuta/profile.md", "Stack: Go\nTemplate: templates/go.md\nLint: "+tc.command+"\n")
			calls := 0
			useReviewRunner(&opts, func(context.Context, publication.Command) (publication.CommandResult, error) {
				calls++
				return cleanReview(), nil
			})
			results, err := RunCohorts(t.Context(), manifest, runtime, opts)
			if (err != nil) != tc.fails {
				t.Fatalf("err=%v", err)
			}
			if tc.fails && calls != 0 {
				t.Fatal("review ran after lint failure")
			}
			report := BuildReport(manifest, results, nil)
			if tc.fails && (report.Verdict != Rework || report.Suppressed != 0) {
				t.Fatalf("report=%+v", report)
			}
			var out bytes.Buffer
			if err := PrintReport(&out, report); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q: %s", tc.want, out.String())
			}
			if tc.fails && !strings.Contains(out.String(), "missing-linter") {
				t.Fatalf("missing failure detail: %s", out.String())
			}
			dir := t.TempDir()
			if err := WriteArtifacts(dir, report, IncrementalState{}); err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(filepath.Join(dir, "review.md"))
			if err != nil || !strings.Contains(string(payload), tc.want) {
				t.Fatalf("artifact status=%s err=%v", payload, err)
			}
		})
	}
}

func TestSuppressOnlyMatchingRule(t *testing.T) {
	finding := Finding{File: "src/a.go", Line: 10, EndLine: 12, Rule: "  Lint/Rule  ", Severity: Blocker}
	for _, tc := range []struct {
		name, rule, file string
		line, end        int
		suppressed       bool
	}{
		{"same rule", "lint/rule", "src/a.go", 12, 14, true},
		{"unrelated", "style/rule", "src/a.go", 10, 10, false},
		{"unknown", "", "src/a.go", 10, 10, false},
		{"blank", "  ", "src/a.go", 10, 10, false},
		{"different file", "lint/rule", "src/b.go", 10, 10, false},
		{"outside", "lint/rule", "src/a.go", 13, 14, false},
		{"invalid range", "lint/rule", "src/a.go", 12, 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diagnostic := LinterFinding{File: tc.file, Line: tc.line, EndLine: tc.end, Rule: tc.rule}
			for _, merge := range []func([]Finding, []LinterFinding) MergeResult{suppressLintOverlaps, Merge} {
				got := merge([]Finding{finding}, []LinterFinding{diagnostic})
				if (len(got.Suppressed) == 1) != tc.suppressed || len(got.Findings)+len(got.Suppressed) != 1 {
					t.Fatalf("result=%+v", got)
				}
			}
		})
	}
	finding.Rule = " "
	if got := suppressLintOverlaps([]Finding{finding}, []LinterFinding{{File: finding.File, Line: 10, Rule: " "}}); len(got.Findings) != 1 {
		t.Fatal("suppressed unknown rule")
	}
}
