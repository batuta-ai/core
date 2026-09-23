package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/review"
	"github.com/batuta-ai/core/routing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "capabilities" || os.Args[1] == "review" || os.Args[1] == "loop") {
		if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			var exit *ExitError
			if errors.As(err, &exit) {
				os.Exit(exit.Code)
			}
			fmt.Fprintln(os.Stderr, "batuta:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRunRequiresASubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err == nil {
		t.Fatal("run() with no arguments should fail")
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func TestDispatchWorker(t *testing.T) {
	if os.Getenv("BATUTA_DISPATCH_FIXTURE") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) != 5 || args[2] != "chosen-model" || args[3] != "high" {
		os.Exit(91)
	}
	cwd, _ := os.Getwd()
	want, _ := filepath.EvalSymlinks(args[4])
	if cwd != want {
		os.Exit(92)
	}
	f, err := os.OpenFile("calls", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(93)
	}
	f.WriteString("call\n")
	f.Close()
	fmt.Fprintln(os.Stderr, "worker stderr")
	switch args[1] {
	case "fail":
		os.Exit(7)
	case "question":
		fmt.Println("BATUTA-QUESTION: choose a path?")
	case "limit":
		fmt.Println("usage limit reached")
		os.Exit(1)
	default:
		fmt.Println("worker completed")
	}
	os.Exit(0)
}

func dispatchCommandFixture(t *testing.T, brief string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	skills := t.TempDir()
	if err := os.Mkdir(filepath.Join(skills, "adapters"), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := fmt.Sprintf("---\nname: fixture\nrun: '%s '-test.run=^TestDispatchWorker$' -- {brief} {model_flags} {cwd}'\nmodel_flags: '--unused'\nreadonly: unused\navailable: must-never-run\nmodels: must-never-run\nfinished: exit_code\nlimit_regex: usage limit reached\n---\n", "\""+os.Args[0]+"\"")
	adapter = strings.Replace(adapter, "--unused", "{model} {effort}", 1)
	if err := os.WriteFile(filepath.Join(skills, "adapters", "fixture.md"), []byte(adapter), 0o600); err != nil {
		t.Fatal(err)
	}
	briefPath := filepath.Join(root, "brief.md")
	if err := os.WriteFile(briefPath, []byte(brief), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATUTA_SKILLS", skills)
	t.Setenv("BATUTA_DISPATCH_FIXTURE", "1")
	return root, []string{"dispatch", "--brief-file", briefPath, "--executor", "fixture", "--model", "chosen-model", "--effort", "high", "--cwd", root}
}

func TestDispatchCommandReportsOneAttempt(t *testing.T) {
	for _, tc := range []struct {
		brief, mode, class string
		code               int
	}{
		{"success", "", "completed", 0}, {"success", "cli", "completed", 0},
		{"success", "auto", "completed", 0}, {"fail", "cli", "failed", 1},
		{"question", "cli", "waiting_input", 3}, {"limit", "auto", "rate_limited", 4},
		{"success", "acp", "unavailable", 2},
	} {
		t.Run(tc.brief+tc.mode, func(t *testing.T) {
			root, args := dispatchCommandFixture(t, tc.brief)
			if tc.mode != "" {
				args = append(args, "--transport", tc.mode)
			}
			var stdout, stderr bytes.Buffer
			err := run(args, &stdout, &stderr)
			var exit *ExitError
			if tc.code == 0 && err != nil || tc.code != 0 && (!errors.As(err, &exit) || exit.Code != tc.code) {
				t.Fatalf("exit = %v; stdout=%s stderr=%s", err, &stdout, &stderr)
			}
			var report struct {
				ExitClass string `json:"exit_class"`
				ExitCode  int    `json:"exit_code"`
				Backend   string `json:"backend"`
				Artifacts struct {
					Directory string `json:"directory"`
				} `json:"artifacts"`
				Receipt executor.Receipt `json:"receipt"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatal(err, stdout.String())
			}
			if report.ExitClass != tc.class || report.ExitCode != tc.code || stdout.Len() > executor.ReceiptLimit || strings.Contains(stdout.String(), "worker completed") {
				t.Fatalf("report = %s", &stdout)
			}
			if report.Artifacts.Directory == "" {
				t.Fatal("missing evidence directory")
			}
			t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
			for _, name := range []string{"brief.md", "intent.json", "receipt.json", "stdout.log", "stderr.log"} {
				info, err := os.Lstat(filepath.Join(report.Artifacts.Directory, name))
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
					t.Fatalf("artifact %s: %v, %v", name, info, err)
				}
			}
			persisted, err := os.ReadFile(filepath.Join(report.Artifacts.Directory, "receipt.json"))
			if err != nil || !bytes.Equal(bytes.TrimSpace(persisted), bytes.TrimSpace(stdout.Bytes())) {
				t.Fatalf("persisted report differs: %s, %v", persisted, err)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if tc.mode == "acp" {
				if !os.IsNotExist(err) || report.Receipt.Submission.State != executor.SubmissionNotSubmitted {
					t.Fatalf("ACP ran or lost non-submission: %s %+v", calls, report)
				}
			} else if err != nil || string(calls) != "call\n" || report.Backend != "cli" || report.Receipt.Submission.State != executor.SubmissionSubmitted {
				t.Fatalf("attempts=%q, report=%+v, err=%v", calls, report, err)
			}
		})
	}
}

func TestDispatchAdapterMetadataCannotQualifyNativeLaunch(t *testing.T) {
	for _, mode := range []string{"acp", "auto"} {
		t.Run(mode, func(t *testing.T) {
			root, args := dispatchCommandFixture(t, "success")
			adapterPath := filepath.Join(os.Getenv("BATUTA_SKILLS"), "adapters", "fixture.md")
			data, err := os.ReadFile(adapterPath)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte("name: fixture"), []byte("name: codex\nacp_run: codex-acp\nacp_version: native-fixture-1"), 1)
			if err := os.WriteFile(filepath.Join(filepath.Dir(adapterPath), "codex.md"), data, 0600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--executor", "codex", "--transport", mode)
			var stdout, stderr bytes.Buffer
			err = run(args, &stdout, &stderr)
			var report executor.DispatchReport
			if decodeErr := json.Unmarshal(stdout.Bytes(), &report); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if report.Artifacts.Directory != "" {
				t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
			}
			calls, readErr := os.ReadFile(filepath.Join(root, "calls"))
			if mode == "acp" {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 2 || report.ExitClass != "unavailable" || report.Receipt.Submission.State != executor.SubmissionNotSubmitted || !os.IsNotExist(readErr) {
					t.Fatalf("metadata qualified native launch: %+v / %v; calls=%s", report, err, calls)
				}
			} else if err != nil || report.Backend != "cli" || report.ExitClass != "completed" || readErr != nil || string(calls) != "call\n" {
				t.Fatalf("auto fallback changed: %+v / %v; calls=%s", report, err, calls)
			}
		})
	}
}

func TestDispatchInvalidArgumentsNeverRunWorker(t *testing.T) {
	for _, extra := range [][]string{
		{"--transport", "native"}, {"--transport", "bogus"}, {"--transport", ""},
		{"--executor", "../fixture"}, {"--executor", "native"}, {"--executor", "self"},
		{"--model", ""}, {"--model", "default"}, {"--model", "--injected"},
		{"--effort", "--injected"}, {"--cwd", "missing"}, {"--brief-file", "missing"},
		{"--timeout", "0s"}, {"unexpected"}, {"--receipt-file", "victim"},
	} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			root, args := dispatchCommandFixture(t, "success")
			var stdout, stderr bytes.Buffer
			err := run(append(args, extra...), &stdout, &stderr)
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 2 || !json.Valid(stdout.Bytes()) {
				t.Fatalf("invalid request: %v %s", err, &stdout)
			}
			if _, err := os.Stat(filepath.Join(root, "calls")); !os.IsNotExist(err) {
				t.Fatal("invalid request ran worker")
			}
		})
	}
}

func TestDispatchNativeOpenCodeMissingBinaryStaysUnavailable(t *testing.T) {
	root, args := dispatchCommandFixture(t, "success")
	adapterPath := filepath.Join(os.Getenv("BATUTA_SKILLS"), "adapters", "fixture.md")
	payload, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.Replace(payload, []byte("name: fixture"), []byte("name: opencode\nacp_run: opencode acp\nacp_version: 1.18.31\nacp_model_config: model"), 1)
	if err := os.WriteFile(filepath.Join(filepath.Dir(adapterPath), "opencode.md"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	// Keep the public command's native constructor, but prevent provider launch.
	// The CLI fixture executable is absolute and does not depend on PATH.
	t.Setenv("PATH", t.TempDir())
	args = append(args, "--executor", "opencode", "--model", "opencode/big-pickle", "--effort", "", "--transport", "acp")
	var stdout, stderr bytes.Buffer
	err = run(args, &stdout, &stderr)
	var report executor.DispatchReport
	if decodeErr := json.Unmarshal(stdout.Bytes(), &report); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if report.Artifacts.Directory != "" {
		t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
	}
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 || report.ExitClass != "unavailable" || report.Executor != "opencode" || report.Model != "opencode/big-pickle" || report.Effort != "" || report.Receipt.Submission.State != executor.SubmissionNotSubmitted || report.Artifacts.Directory == "" {
		t.Fatalf("missing native provider was not rejected: %+v / %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(root, "calls")); !os.IsNotExist(err) {
		t.Fatal("unavailable explicit ACP ran CLI")
	}
}

func TestDispatchNativeOpenCodeLaunchEvidence(t *testing.T) {
	for _, model := range []string{"opencode/big-pickle", "opencode/other"} {
		t.Run(model, func(t *testing.T) {
			root, args := dispatchCommandFixture(t, "success")
			bin := t.TempDir()
			// Only shell builtins are needed. Every invocation and input line is
			// recorded before failure, including any attempted CLI replay.
			fixture := `#!/bin/sh
printf '%s\n' "$*" >> native-calls || exit 91
case "$*" in
  --version) printf '1.18.31\n' ;;
  acp)
    while IFS= read -r line; do
      printf '%s\n' "$line" >> native-input || exit 92
      printf 'invalid-acp\n' || exit 93
    done
    exit 7 ;;
  *) exit 7 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(fixture), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			adapter := "---\nname: opencode\nrun: 'opencode run {brief} {model_flags} {cwd}'\nmodel_flags: '{model} {effort}'\nreadonly: unused\navailable: must-never-run\nmodels: must-never-run\nfinished: exit_code\nacp_run: opencode acp\nacp_version: 1.18.31\nacp_model_config: model\n---\n"
			if err := os.WriteFile(filepath.Join(os.Getenv("BATUTA_SKILLS"), "adapters", "opencode.md"), []byte(adapter), 0600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--executor", "opencode", "--model", model, "--effort", "", "--transport", "acp", "--timeout", "2s")
			var stdout, stderr bytes.Buffer
			err := run(args, &stdout, &stderr)
			var report executor.DispatchReport
			if decodeErr := json.Unmarshal(stdout.Bytes(), &report); decodeErr != nil {
				t.Fatalf("dispatch report: %v; stdout=%s stderr=%s", decodeErr, &stdout, &stderr)
			}
			if report.Artifacts.Directory != "" {
				t.Cleanup(func() {
					if err := os.RemoveAll(report.Artifacts.Directory); err != nil {
						t.Error(err)
					}
				})
			}
			if err == nil || report.Executor != "opencode" || report.Model != model || report.Effort != "" || report.Receipt.Submission.State != executor.SubmissionNotSubmitted || report.Backend == "cli" || report.Artifacts.Directory == "" {
				t.Fatalf("fixture failure submitted or replayed work: %+v / %v", report, err)
			}
			calls, callsErr := os.ReadFile(filepath.Join(root, "native-calls"))
			input, inputErr := os.ReadFile(filepath.Join(root, "native-input"))
			if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
				if report.Backend != "acp" || report.Receipt.Transport.Failure != "protocol" || callsErr != nil || string(calls) != "--version\nacp\n" || inputErr != nil {
					t.Fatalf("native route did not reach fixture protocol failure: %+v / %v; calls=%q / %v input=%q / %v", report, err, calls, callsErr, input, inputErr)
				}
				var request struct{ Method string }
				if err := json.Unmarshal(bytes.TrimSpace(input), &request); err != nil || request.Method != "initialize" {
					t.Fatalf("expected only initialization, without a task prompt: %q / %v", input, err)
				}
			} else {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 2 || report.ExitClass != "unavailable" || !os.IsNotExist(callsErr) || !os.IsNotExist(inputErr) {
					t.Fatalf("unqualified platform/model launched provider: %+v / %v; calls=%q / %v input=%q / %v", report, err, calls, callsErr, input, inputErr)
				}
			}
		})
	}
}

func TestDispatchMissingBinaryNeverRunsDiscoveryOrInstall(t *testing.T) {
	root, args := dispatchCommandFixture(t, "success")
	adapterPath := filepath.Join(os.Getenv("BATUTA_SKILLS"), "adapters", "fixture.md")
	payload, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.ReplaceAll(payload, []byte(os.Args[0]), []byte(filepath.Join(root, "not-installed")))
	if err := os.WriteFile(adapterPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = run(args, &stdout, &stderr)
	var report struct {
		ExitClass string `json:"exit_class"`
		Artifacts struct {
			Directory string `json:"directory"`
		} `json:"artifacts"`
	}
	if err == nil || json.Unmarshal(stdout.Bytes(), &report) != nil || report.ExitClass != "unavailable" {
		t.Fatalf("missing binary = %v %s", err, &stdout)
	}
	if report.Artifacts.Directory != "" {
		t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
	}
	if _, err := os.Stat(filepath.Join(root, "calls")); !os.IsNotExist(err) {
		t.Fatal("missing binary triggered worker")
	}
}

func TestLoopTransportSelection(t *testing.T) {
	for _, mode := range []string{"cli", "acp", "auto", "native", "bogus", ""} {
		var stdout, stderr bytes.Buffer
		err := run([]string{"loop", "--transport", mode, "--workspace", t.TempDir(), "--dry-run"}, &stdout, &stderr)
		want := "not a git repository"
		if mode == "native" || mode == "bogus" || mode == "" {
			want = "transport must be cli, acp or auto"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("transport %q = %v, want %q", mode, err, want)
		}
	}
}

func TestRunRejectsUnknownSubcommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"conduct"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "conduct") {
		t.Fatalf("run(conduct) error = %v, want unknown subcommand", err)
	}
}

func TestRunPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(version) error = %v", err)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("version printed nothing")
	}
}

func TestRunPrintsCapabilities(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(capabilities) error = %v", err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("capabilities is not JSON: %v\n%s", err, stdout.String())
	}
	if got.Version == "" {
		t.Fatal("capabilities.version is empty")
	}
	for _, want := range []string{"capabilities", "doctor", "gate", "inventory", "loop", "roadmap", "trail", "version"} {
		if !slices.Contains(got.Commands, want) {
			t.Fatalf("capabilities.commands = %v, missing %q", got.Commands, want)
		}
	}
}

func TestCapabilitiesListsRoadmap(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(capabilities) error = %v", err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("capabilities is not JSON: %v\n%s", err, stdout.String())
	}
	if !slices.Contains(got.Commands, "roadmap") {
		t.Fatalf("capabilities.commands = %v, missing %q", got.Commands, "roadmap")
	}
}

func TestCapabilitiesListsWatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Commands, "watch") {
		t.Fatalf("capabilities.commands = %v, missing watch", got.Commands)
	}
	if !strings.Contains(usage, "batuta watch") {
		t.Fatal("usage is missing batuta watch")
	}
}

func TestUsageListsEveryGateForm(t *testing.T) {
	for _, want := range []string{
		"batuta gate tree",
		"batuta gate tests",
		"batuta gate scope",
		"batuta gate proofs",
		"batuta gate verifier",
	} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
}

func TestUsageListsWatchFlagsAndKeys(t *testing.T) {
	for _, want := range []string{"--interval", "--once", "--lang", "--ascii", "Keys: Up/Down and PgUp/PgDn scroll", "f follows", "r opens", "R shows", "d opens", "ctrl+enter", "alt+enter", "ctrl+s", "o opens", "l changes", "? shows", "q quits", "mouse wheel"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage is missing watch option or key %q", want)
		}
	}
}

func TestCapabilitiesListsReview(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Commands, "review") {
		t.Fatalf("capabilities.commands = %v, missing review", got.Commands)
	}
}

func TestUsage(t *testing.T) {
	for _, want := range []string{"batuta review", "--base", "--worktree", "--spec", "--cohort-files", "--parallel", "--reviewer", "--full", "--out"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
}

func TestReviewWritesArtefacts(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	out := filepath.Join(root, "artifacts")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"review", "--base", base, "--out", out}, &stdout, &stderr); err != nil {
		t.Fatalf("review = %v\n%s", err, &stderr)
	}
	for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	printed, err := os.ReadFile(filepath.Join(out, "review.md"))
	if err != nil || string(printed) != stdout.String() {
		t.Fatalf("review.md does not match stdout: %v\nfile=%q\nout=%q", err, printed, stdout.String())
	}
}

func TestReviewExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		finding  *review.Finding
		wantCode int
	}{
		{name: "ship", wantCode: 0},
		{name: "fix before ship", finding: &review.Finding{Severity: review.Major, Kind: review.Defect, File: "change.go", Line: 3, Premise: "Wrong result", Path: "Call path", Verdict: "Fails", Fix: "Correct it"}, wantCode: 2},
		{name: "rework", finding: &review.Finding{Severity: review.Blocker, Kind: review.Defect, File: "change.go", Line: 3, Premise: "Build breaks", Path: "Compile", Verdict: "Fails", Fix: "Restore it"}, wantCode: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, base := reviewCommandRepo(t)
			t.Chdir(root)
			var findings []review.Finding
			if tc.finding != nil {
				findings = []review.Finding{*tc.finding}
			}
			restore := stubReviewSessions(t, findings, nil)
			defer restore()
			var stdout, stderr bytes.Buffer
			err := run([]string{"review", "--base", base, "--out", filepath.Join(root, "out")}, &stdout, &stderr)
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("review = %v", err)
				}
				return
			}
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != tc.wantCode {
				t.Fatalf("review error = %#v, want exit %d", err, tc.wantCode)
			}
		})
	}
}

func TestReviewPrintsReport(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	finding := review.Finding{Severity: review.Minor, Kind: review.Advisory, File: "change.go", Line: 3, Premise: "Name is vague", Path: "Rename", Fix: "Use a precise name"}
	restore := stubReviewSessions(t, []review.Finding{finding}, nil)
	defer restore()
	var stdout, stderr bytes.Buffer
	if err := run([]string{"review", "--base", base, "--out", filepath.Join(root, "out")}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Files:", "Cohorts:", "Coverage:", "minor · change.go:3 · Name is vague · Use a precise name", "Suppressed overlaps: 0", "Verdict: SHIP"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("report is missing %q:\n%s", want, &stdout)
		}
	}
}

func TestReviewRefusesTreeChange(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	restore := stubReviewRunner(t, func(_ context.Context, _ publication.Command) (publication.CommandResult, error) {
		if err := os.WriteFile(filepath.Join(root, "tracked.go"), []byte("package changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
	})
	defer restore()
	var stdout, stderr bytes.Buffer
	err := run([]string{"review", "--base", base, "--out", filepath.Join(root, "out")}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "tracked.go") || strings.Contains(err.Error(), "change.go") {
		t.Fatalf("review error = %v, want changed tracked path", err)
	}
}

func reviewCommandRepo(t *testing.T) (string, string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Review Test", "GIT_AUTHOR_EMAIL=review@example.test", "GIT_COMMITTER_NAME=Review Test", "GIT_COMMITTER_EMAIL=review@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-q")
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".gitignore":         ".batuta/reviews/\nout/\nartifacts/\n",
		".batuta/profile.md": "Stack: Go\nMethodology: TDD\nTest: go test ./...\nTemplate: templates/generic.md\n",
		".batuta/routing.md": "| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| high | * | codex | review-model |\n",
		"tracked.go":         "package tracked\n",
		"change.go":          "package changed\n",
	}
	for name, payload := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit("add", ".")
	runGit("-c", "commit.gpgsign=false", "commit", "-qm", "base")
	base := runGit("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "change.go"), []byte("package changed\n\nvar Changed = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, base
}

func stubReviewSessions(t *testing.T, findings []review.Finding, sweep *review.SpecSweep) func() {
	t.Helper()
	return stubReviewRunner(t, func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		if len(command.Args) == 0 || command.Args[0] != "--read-only" || command.Directory == "" {
			t.Fatalf("review invocation = %+v", command)
		}
		if sweep != nil && strings.Contains(command.Args[len(command.Args)-1], "<<<CRITERIA") {
			var lines strings.Builder
			lines.WriteString("<<<CRITERIA\n")
			for _, result := range sweep.Results {
				payload, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				lines.Write(payload)
				lines.WriteByte('\n')
			}
			lines.WriteString("CRITERIA>>>\n")
			return publication.CommandResult{Stdout: []byte(lines.String())}, nil
		}
		var output strings.Builder
		output.WriteString("<<<FINDINGS\n")
		for _, finding := range findings {
			payload, err := json.Marshal(finding)
			if err != nil {
				t.Fatal(err)
			}
			output.Write(payload)
			output.WriteByte('\n')
		}
		output.WriteString("FINDINGS>>>\n")
		return publication.CommandResult{Stdout: []byte(output.String())}, nil
	})
}

type mainReviewRunner func(context.Context, publication.Command) (publication.CommandResult, error)

func (f mainReviewRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	return f(ctx, command)
}

func stubReviewRunner(t *testing.T, runner mainReviewRunner) func() {
	t.Helper()
	skills := t.TempDir()
	for name, payload := range map[string]string{
		"adapters/codex.md":    "---\nname: codex\nrun: fake-reviewer --write {brief}\nreadonly: fake-reviewer --read-only {model_flags} {prompt}\nmodel_flags: --model {model}\navailable: fake-reviewer --version\nmodels: fake-reviewer models\nfinished: exit_code\n---\n",
		"templates/generic.md": "## Conventions for briefs\nKeep changes scoped.\n",
	} {
		path := filepath.Join(skills, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := reviewSessionOptions
	reviewSessionOptions = func(root string, parallel int) review.SessionOptions {
		return review.SessionOptions{
			Root: root, Skills: skills, Parallel: parallel,
			Subprocess: &executor.Subprocess{Runner: runner, Lookup: func(string) (string, error) { return filepath.Join(skills, "fake-reviewer"), nil }},
		}
	}
	return func() { reviewSessionOptions = previous }
}

func TestWatchHelpListsFlagsAndKeys(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"watch", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("watch --help = %v", err)
	}
	for _, want := range []string{"batuta watch", "--interval", "--once", "--lang", "--ascii", "Keys: Up/Down and PgUp/PgDn scroll", "f follows", "r opens", "R shows", "d opens", "ctrl+enter", "alt+enter", "ctrl+s", "o opens", "l changes", "? shows", "q quits", "mouse wheel"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("watch --help is missing %q\n%s", want, &stderr)
		}
	}
}

func TestInspectGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if top, state, clean := inspectGit(ctx, git, root); top != "" || state != "" || clean != nil {
		t.Fatalf("inspectGit(non-repo) = %q, %q, %v; want empty", top, state, clean)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
		{"config", "gc.autoDetach", "false"},
		{"config", "maintenance.auto", "false"},
	} {
		if out, err := exec.Command(git, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	top, state, clean := inspectGit(ctx, git, root)
	if top != root || state != "clean" || clean == nil || !*clean {
		t.Fatalf("inspectGit(fresh repo) = %q, %q, %v; want %q, clean, true", top, state, clean, root)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	top, state, clean = inspectGit(ctx, git, nested)
	if top != root || state != "dirty" || clean == nil || *clean {
		t.Fatalf("inspectGit(nested, dirty) = %q, %q, %v; want toplevel %q, dirty, false", top, state, clean, root)
	}
}

func TestInspectGitTellsManagedStateApart(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
		{"config", "gc.autoDetach", "false"},
		{"config", "maintenance.auto", "false"},
	} {
		if out, err := exec.Command(git, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "WORK.md"), []byte("managed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	managedDir := filepath.Join(root, ".batuta")
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managedDir, "state.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	top, state, clean := inspectGit(context.Background(), git, root)
	if top != root || state != "managed" || clean == nil || *clean {
		t.Fatalf("inspectGit(managed) = %q, %q, %v; want %q, managed, false", top, state, clean, root)
	}
	var output bytes.Buffer
	printDoctor(&output, doctorReport{
		GitRepository: true,
		GitToplevel:   root,
		GitState:      state,
		GitClean:      clean,
	})
	if !strings.Contains(output.String(), "managed state only (WORK.md, .batuta/) — fine for /batuta, commit before batuta loop") {
		t.Fatalf("printDoctor(managed) = %q, want managed-state guidance", output.String())
	}
}

func TestInspectGitWithSpentContext(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if out, err := exec.Command(git, "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
		{"config", "gc.autoDetach", "false"},
		{"config", "maintenance.auto", "false"},
	} {
		if out, err := exec.Command(git, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	spent, cancel := context.WithCancel(context.Background())
	cancel()
	if top, _, _ := inspectGit(spent, git, root); top != "" {
		t.Fatalf("inspectGit with a cancelled context = %q; a spent context must not report a repository, doctor must give git its own", top)
	}
}

func TestDoctorPrintsCleanAndDirtyGitStates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state string
		clean bool
		want  string
	}{
		{name: "clean", state: "clean", clean: true, want: "clean tree"},
		{name: "dirty", state: "dirty", clean: false, want: "dirty tree — commit or stash before delegating"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			printDoctor(&output, doctorReport{
				GitRepository: true,
				GitToplevel:   "/repo",
				GitState:      tc.state,
				GitClean:      &tc.clean,
			})
			if !strings.Contains(output.String(), tc.want) {
				t.Fatalf("printDoctor(%s) = %q, want %q", tc.state, output.String(), tc.want)
			}
		})
	}
}

func TestDoctorNotesSlowProbe(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	printDoctor(&output, doctorReport{ProbeDurations: []doctorProbeDuration{
		{Executor: "cursor-agent", Probe: "models", Duration: 7200 * time.Millisecond},
		{Executor: "codex", Probe: "--version", Duration: 5 * time.Second},
		{Executor: "opencode", Probe: "status", Duration: 4900 * time.Millisecond},
	}})
	if got := strings.Count(output.String(), "note:"); got != 1 {
		t.Fatalf("slow-probe notes = %d, want 1:\n%s", got, output.String())
	}
	if !strings.Contains(output.String(), "note: cursor-agent models took 7.2s (budget 5s)") {
		t.Fatalf("printDoctor() = %q, want slow-probe note", output.String())
	}
}

func TestVersionPrefersTheBuildVersion(t *testing.T) {
	previous := buildVersion
	t.Cleanup(func() { buildVersion = previous })
	buildVersion = "v9.9.9-beta.1"
	if got := version(); got != "v9.9.9-beta.1" {
		t.Fatalf("version() = %q, want the ldflags value", got)
	}
	buildVersion = ""
	if got := version(); got == "" || got == "v9.9.9-beta.1" {
		t.Fatalf("version() without ldflags = %q", got)
	}
}

func TestLoopDryRunReportsJudge(t *testing.T) {
	writeJudgeConfig := func(t *testing.T, root, config string) {
		t.Helper()
		if config == "" {
			return
		}
		if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".batuta", "judge.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name   string
		config string
		envOff bool
		want   string
	}{
		{name: "off by default", want: "judge     judge_off"},
		{name: "typesafe", config: `{"provider":"typesafe","model":"jev-latest","key_env":"JUDGE_TEST_KEY"}`, want: "judge     typesafe"},
		{name: "auto chain", config: `{"provider":"auto"}`, want: "judge     typesafe,vercel,openrouter"},
		{name: "env off", config: `{"provider":"typesafe","model":"jev-latest","key_env":"JUDGE_TEST_KEY"}`, envOff: true, want: "judge     judge_off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeJudgeConfig(t, root, tc.config)
			if tc.envOff {
				t.Setenv("BATUTA_JUDGE", "off")
			} else {
				t.Setenv("BATUTA_JUDGE", "")
			}
			t.Setenv("JUDGE_TEST_KEY", "")
			var stdout, stderr bytes.Buffer
			err := run([]string{"loop", "--workspace", root, "--dry-run"}, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "not a git repository") {
				t.Fatalf("dry-run error = %v\n%s", err, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tc.want)
			}
			if strings.Contains(stdout.String(), "JUDGE_TEST_KEY=") || strings.Contains(stderr.String(), "JUDGE_TEST_KEY=") {
				t.Fatalf("judge dry-run leaked a key env assignment:\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
			}
		})
	}
}

func TestLoopSubcommandRefusesToRunOutsideAPreparedWorkspace(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := run([]string{"loop", "--workspace", root, "--dry-run"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("loop in a non-repository error = %v", err)
	}
	if err := run([]string{"trail", "--workspace", root}, &stdout, &stderr); err == nil {
		t.Fatal("trail without journals should fail")
	}
	if err := run([]string{"loop", "--workspace", root, "--dashboard"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "no open deliveries") {
		t.Fatalf("dashboard without journals = %v\n%s", err, stdout.String())
	}
}

func TestLoopRoadmapDryRunPrintsTheChain(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta", "plans", "done"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"roadmap.md":             "# Roadmap — Delivery\n\n- [x] 1. Finished → plans/finished.md\n- [ ] 2. Ready → plans/ready.md\n- [ ] 3. Draft → plans/draft.md\n- [ ] 4. Missing → plans/missing.md\n- [ ] 5. Unplanned\n",
		"plans/done/finished.md": "archived",
	}
	for slug, status := range map[string]string{"ready": "approved", "draft": "proposed"} {
		files["plans/"+slug+".md"] = "# Plan — " + slug + "\n\n**Goal:** Deliver.\n**Created:** 2026-09-06 · **Status:** " + status + "\n\n## Tasks\n- [ ] 1. Build — backend/low\n      Scope: out.txt\n      Accept: exists → test -f out.txt\n"
	}
	for name, payload := range files {
		if err := os.WriteFile(filepath.Join(root, ".batuta", name), []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"loop", "--workspace", root, "--dry-run", "--roadmap"}, &stdout, &stderr); err != nil {
		t.Fatalf("roadmap dry run = %v\n%s", err, &stderr)
	}
	previous := -1
	for _, want := range []string{
		"roadmap Delivery",
		"1. Finished → plans/finished.md: done",
		"2. Ready → plans/ready.md: approved",
		"3. Draft → plans/draft.md: waiting_plan (proposed)",
		"4. Missing → plans/missing.md: waiting_plan (missing)",
		"5. Unplanned → (no plan): waiting_plan (missing)",
	} {
		index := strings.Index(stdout.String(), want)
		if index <= previous {
			t.Fatalf("missing or out of order %q:\n%s", want, &stdout)
		}
		previous = index
	}
	if err := run([]string{"loop", "--workspace", root, "--dry-run", "--roadmap", "--answer", "1", "hello"}, &stdout, &stderr); err != nil {
		t.Fatalf("roadmap dry run must not record an answer: %v", err)
	}
	for name, want := range files {
		if got, err := os.ReadFile(filepath.Join(root, ".batuta", name)); err != nil || string(got) != want {
			t.Errorf("dry run changed %s: %q, %v", name, got, err)
		}
	}
	for _, name := range []string{journal.Dir, ".batuta/worktrees", "WORK.md", "out.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("dry run created %s: %v", name, err)
		}
	}
}

func TestLoopRoadmapWaitingPlanExitCode(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".batuta", "roadmap.md"), []byte("# Roadmap — Delivery\n\n- [ ] 1. Missing → plans/missing.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "loop", "--workspace", root, "--roadmap")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "BATUTA_SKILLS=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+tempDir(t))
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 4 {
		t.Fatalf("waiting plan exit = %v, want code 4, waiting_plan\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("waiting_plan")) {
		t.Fatalf("waiting_plan was not printed: %s", output)
	}
	if _, err := os.Stat(filepath.Join(root, journal.Dir)); !os.IsNotExist(err) {
		t.Fatalf("waiting plan opened a journal: %v", err)
	}
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func watchDelivery(t *testing.T, root, delivery string) (*journal.Store, json.RawMessage) {
	t.Helper()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := json.Marshal(routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskPending}}})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(map[string]string{"slug": delivery, "workspace": root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(delivery, journal.Record{Kind: loop.KindOpened, Detail: detail, Graph: graph}); err != nil {
		t.Fatal(err)
	}
	return store, graph
}

type watchCompletionWriter struct {
	buffer   bytes.Buffer
	complete func() error
}

func (w *watchCompletionWriter) Write(p []byte) (int, error) {
	if w.complete != nil {
		complete := w.complete
		w.complete = nil
		if err := complete(); err != nil {
			return 0, err
		}
	}
	return w.buffer.Write(p)
}

func (w *watchCompletionWriter) String() string { return w.buffer.String() }

func TestWatchOpensTheLiveDashboard(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"latest open delivery", []string{"watch", "--interval", "1ms"}},
		{"delivery before flags", []string{"watch", "demo", "--interval", "1ms"}},
		{"delivery after flags", []string{"watch", "--interval", "1ms", "demo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", "en")
			root := t.TempDir()
			t.Chdir(root)
			store, graph := watchDelivery(t, root, "older")
			old := time.Unix(1, 0)
			if err := os.Chtimes(store.Path("older"), old, old); err != nil {
				t.Fatal(err)
			}
			watchDelivery(t, root, "demo")
			watchDelivery(t, root, "closed")
			if _, err := store.Append("closed", journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`), Graph: graph}); err != nil {
				t.Fatal(err)
			}
			stdout := &watchCompletionWriter{complete: func() error {
				_, err := store.Append("demo", journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`), Graph: graph})
				return err
			}}
			var stderr bytes.Buffer
			if err := run(tc.args, stdout, &stderr); err != nil {
				t.Fatalf("watch = %v\nstderr: %s", err, &stderr)
			}
			if got := stdout.String(); strings.Count(got, "batuta watch") != 2 || strings.Contains(got, "\x1b") || !strings.Contains(got, "batuta watch · demo") || strings.Contains(got, "batuta watch · older") || strings.Contains(got, "batuta watch · closed") {
				t.Fatalf("watch must redraw the selected delivery through completion:\n%s", got)
			}
		})
	}
}

func TestWatchOncePrintsASnapshot(t *testing.T) {
	for _, args := range [][]string{{"watch", "--once"}, {"watch", "demo", "--once"}, {"watch", "--once", "demo"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			watchDelivery(t, root, "demo")
			var stdout, stderr bytes.Buffer
			if err := run(args, &stdout, &stderr); err != nil {
				t.Fatalf("watch --once = %v\nstderr: %s", err, &stderr)
			}
			if got := stdout.String(); strings.Count(got, "batuta watch") != 1 || !strings.Contains(got, "task_1") || !strings.Contains(got, "demo") || strings.Contains(got, "\x1b[") {
				t.Fatalf("want one panel snapshot, got %q", got)
			}
		})
	}
}

func TestWatchLanguageAndASCII(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		want   string
		border string
	}{
		{"Portuguese ASCII", []string{"--lang", "pt", "--ascii"}, "Progresso", "+--"},
		{"English Unicode", []string{"--lang", "en"}, "Progress", "┌─"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", "pt")
			t.Setenv("LC_ALL", "en_US.UTF-8")
			root := t.TempDir()
			t.Chdir(root)
			store, graph := watchDelivery(t, root, "demo")
			if _, err := store.Append("demo", journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`), Graph: graph}); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if err := run(append([]string{"watch", "demo"}, tc.args...), &stdout, &stderr); err != nil {
				t.Fatalf("watch = %v\nstderr: %s", err, &stderr)
			}
			if got := stdout.String(); !strings.Contains(got, tc.want) || !strings.Contains(got, tc.border) {
				t.Fatalf("missing %q or %q in panel:\n%s", tc.want, tc.border, got)
			}
		})
	}
}

func TestWatchRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"watch", "--lang", "es"},
		{"watch", "--lang", ""},
		{"watch", "demo", "--interval", "invalid"},
		{"watch", "demo", "extra"},
		{"watch", "demo", "--unknown"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(args, &stdout, &stderr); err == nil {
			t.Errorf("run(%q) accepted invalid arguments", args)
		}
	}
}

func TestLoopDashboardStillWorks(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	graph := routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskIntegrated}}}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	appendRecord := func(kind journal.Kind, detail string) {
		t.Helper()
		if _, err := store.Append("demo", journal.Record{Kind: kind, Detail: json.RawMessage(detail), Graph: graphJSON}); err != nil {
			t.Fatal(err)
		}
	}
	appendRecord(loop.KindOpened, `{"slug":"demo"}`)
	appendRecord(loop.KindTerminal, `{"state":"done"}`)

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"dashboard remains TSV", []string{"loop", "--workspace", root, "--dashboard", "demo"}, "delivery  state"},
		{"watch renders panel", []string{"loop", "--workspace", root, "--dashboard", "--watch", "--interval", time.Millisecond.String(), "demo"}, "batuta watch · demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if err := run(tc.args, &stdout, &stderr); err != nil {
				t.Fatalf("run() error = %v\nstderr: %s", err, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tc.want)
			}
		})
	}
}

func TestDryRunListsLimitFallbacks(t *testing.T) {
	var out bytes.Buffer
	loop.PrintPreview(&out, loop.Preview{Waves: []loop.PreviewWave{{Number: 1, Tasks: []loop.PreviewTask{
		{ID: "task_1", Executor: "codex", Model: "small", Fallbacks: []string{"claude/large", "self/session"}},
		{ID: "task_2", Executor: "codex", Model: "large"},
	}}}})
	for _, want := range []string{"limit fallback: claude/large", "limit fallback: none"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry run missing %q:\n%s", want, &out)
		}
	}
	var stderr bytes.Buffer
	err := run([]string{"loop", "--workspace", t.TempDir(), "--dry-run", "--limit-horizon", "45m"}, &out, &stderr)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("limit-horizon flag: %v\n%s", err, &stderr)
	}
}

func reviewGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Review Test", "GIT_AUTHOR_EMAIL=review@example.test", "GIT_COMMITTER_NAME=Review Test", "GIT_COMMITTER_EMAIL=review@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func readReviewJSON(t *testing.T, name string, value any) {
	t.Helper()
	payload, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, value); err != nil {
		t.Fatal(err)
	}
}

func onlyReviewStatePath(t *testing.T, root string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, ".batuta", "reviews", "state", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("review state paths=%v err=%v", paths, err)
	}
	return paths[0]
}

func TestReviewStateKeyDistinguishesBranches(t *testing.T) {
	first := reviewStateKey("feature-a", "feature/a", "", "")
	second := reviewStateKey("feature-a", "feature-a", "", "")
	if first == second {
		t.Fatalf("colliding branch keys: %q", first)
	}
	if !strings.HasPrefix(first, "feature-a-") || !strings.HasPrefix(second, "feature-a-") {
		t.Fatalf("keys do not retain sanitized branch name: %q, %q", first, second)
	}

	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	for index, branch := range []string{"feature/a", "feature-a"} {
		if index > 0 {
			reviewGit(t, root, "checkout", "-q", "--detach", base)
		}
		reviewGit(t, root, "checkout", "-qb", branch)
		if err := os.WriteFile(filepath.Join(root, "change.go"), []byte(fmt.Sprintf("package changed\n\nvar Changed = %d\n", index)), 0o644); err != nil {
			t.Fatal(err)
		}
		reviewGit(t, root, "commit", "-qam", branch)
		if err := run([]string{"review", "--base", base, "--out", fmt.Sprintf("out/%d", index)}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("review %s: %v", branch, err)
		}
	}
	paths, err := filepath.Glob(filepath.Join(root, ".batuta", "reviews", "state", "feature-a-*.json"))
	if err != nil || len(paths) != 2 {
		t.Fatalf("branch state paths=%v err=%v", paths, err)
	}
}

func TestReviewStateKeyDistinguishesSpecs(t *testing.T) {
	first := reviewStateKey("feature", "feature", "delivery", "/repo/plans/delivery.md")
	second := reviewStateKey("feature", "feature", "delivery", "/repo/archive/delivery.md")
	if first == second {
		t.Fatalf("colliding spec keys: %q", first)
	}
	if !strings.HasPrefix(first, "feature-delivery-") || !strings.HasPrefix(second, "feature-delivery-") {
		t.Fatalf("keys do not retain sanitized branch and spec names: %q, %q", first, second)
	}

	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "feature")
	reviewGit(t, root, "commit", "-qam", "change")
	plan := "# Plan — Spec\n**Goal:** Review\n**Status:** approved\n## Tasks\n- [ ] 1. Check — docs/low\n      Accept: delivery is reviewed\n"
	var specs []string
	for _, directory := range []string{"one", "two"} {
		name := filepath.Join(root, ".batuta", "plans", directory, "delivery.md")
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(plan), 0o644); err != nil {
			t.Fatal(err)
		}
		specs = append(specs, name)
	}
	restore := stubReviewSessions(t, nil, &review.SpecSweep{Results: []review.SpecResult{{ID: "task-1.1", Status: review.CriterionSatisfied, Path: "change.go:3"}}})
	defer restore()
	for index, spec := range specs {
		if err := run([]string{"review", "--base", base, "--spec", spec, "--out", fmt.Sprintf("out/%d", index)}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("review %s: %v", spec, err)
		}
	}
	paths, err := filepath.Glob(filepath.Join(root, ".batuta", "reviews", "state", "feature-delivery-*.json"))
	if err != nil || len(paths) != 2 {
		t.Fatalf("spec state paths=%v err=%v", paths, err)
	}
}

func TestReviewStateSurvivesDatedDirectories(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "feature/review")
	reviewGit(t, root, "commit", "-qam", "change")
	head := reviewGit(t, root, "rev-parse", "HEAD")
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	previous := reviewNow
	defer func() { reviewNow = previous }()
	for day := 8; day <= 9; day++ {
		reviewNow = func() time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
		if err := run([]string{"review", "--base", base}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	var manifest review.Manifest
	readReviewJSON(t, filepath.Join(root, ".batuta/reviews/2026-09-09-feature-review/manifest.json"), &manifest)
	if manifest.Base != head || len(manifest.Cohorts) != 0 {
		t.Fatalf("second round = %+v, want empty diff from %s", manifest, head)
	}
	var state review.ReviewState
	readReviewJSON(t, onlyReviewStatePath(t, root), &state)
	if state.Head != head {
		t.Fatalf("state = %+v", state)
	}
}

func TestReviewKeepsCheckpointWhenUncovered(t *testing.T) {
	for _, previousRound := range []bool{false, true} {
		t.Run(fmt.Sprint(previousRound), func(t *testing.T) {
			root, base := reviewCommandRepo(t)
			t.Chdir(root)
			reviewGit(t, root, "checkout", "-qb", "pending")
			if previousRound {
				restore := stubReviewSessions(t, nil, nil)
				if err := run([]string{"review", "--base", base, "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
				restore()
			}
			reviewGit(t, root, "commit", "-qam", "change")
			restore := stubReviewRunner(t, func(context.Context, publication.Command) (publication.CommandResult, error) {
				return publication.CommandResult{ExitCode: 1}, nil
			})
			defer restore()
			err := run([]string{"review", "--base", base, "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{})
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 3 {
				t.Fatalf("review = %v", err)
			}
			var state struct {
				Head    string
				Pending []struct{ Files []review.File }
			}
			readReviewJSON(t, onlyReviewStatePath(t, root), &state)
			if state.Head != base || len(state.Pending) != 1 || len(state.Pending[0].Files) != 1 || len(state.Pending[0].Files[0].Hunks) == 0 {
				t.Fatalf("state lost checkpoint or pending hunks: %+v", state)
			}
		})
	}
}

func TestReviewCarriesPendingCohorts(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "pending")
	reviewGit(t, root, "commit", "-qam", "change")
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := stubReviewRunner(t, func(context.Context, publication.Command) (publication.CommandResult, error) {
		return publication.CommandResult{ExitCode: 1}, nil
	})
	err := run([]string{"review", "--base", base, "--worktree", "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{})
	restore()
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("review = %v", err)
	}
	var prompts []string
	restore = stubReviewRunner(t, func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		prompts = append(prompts, command.Args[len(command.Args)-1])
		return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
	})
	defer restore()
	if err := run([]string{"review", "--base", base, "--out", "artifacts"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(prompts, "\n"), "new.go") || !strings.Contains(strings.Join(prompts, "\n"), "change.go") {
		t.Fatalf("pending files not reviewed: %v", prompts)
	}
	var state struct {
		Head    string
		Pending []json.RawMessage
	}
	readReviewJSON(t, onlyReviewStatePath(t, root), &state)
	if state.Head != reviewGit(t, root, "rev-parse", "HEAD") || len(state.Pending) != 0 {
		t.Fatalf("state = %+v", state)
	}
}

func TestReviewWorktreeKeepsBranchBase(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "feature/worktree")
	reviewGit(t, root, "commit", "-qam", "committed change")
	if err := os.WriteFile(filepath.Join(root, "untracked.go"), []byte("package untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	if err := run([]string{"review", "--worktree", "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var manifest review.Manifest
	readReviewJSON(t, filepath.Join(root, "out", "manifest.json"), &manifest)
	if manifest.Base != base {
		t.Fatalf("base=%q, want branch point %q", manifest.Base, base)
	}
	paths := make(map[string]bool)
	for _, file := range manifest.Files {
		paths[file.Path] = true
	}
	if !paths["change.go"] || !paths["untracked.go"] {
		t.Fatalf("manifest paths=%v, want committed and untracked changes", paths)
	}
}

func TestReviewRefusesOutOverTrackedFiles(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
			t.Run(fmt.Sprintf("%s/symlink=%t", name, symlink), func(t *testing.T) {
				root, base := reviewCommandRepo(t)
				t.Chdir(root)
				if err := os.Mkdir(filepath.Join(root, "reports"), 0o755); err != nil {
					t.Fatal(err)
				}
				tracked := filepath.Join(root, "reports", name)
				if err := os.WriteFile(tracked, []byte("preserve source\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				reviewGit(t, root, "add", "reports")
				reviewGit(t, root, "commit", "-qm", "tracked report")
				out := "reports"
				if symlink {
					out = filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(filepath.Join(root, "reports"), out); err != nil {
						t.Fatal(err)
					}
				}
				restore := stubReviewSessions(t, nil, nil)
				defer restore()
				err := run([]string{"review", "--base", base, "--out", out}, &bytes.Buffer{}, &bytes.Buffer{})
				if err == nil || !strings.Contains(err.Error(), "tracked") {
					t.Fatalf("review = %v, want refusal", err)
				}
				payload, readErr := os.ReadFile(tracked)
				if readErr != nil || string(payload) != "preserve source\n" {
					t.Fatalf("tracked source overwritten: %q, %v", payload, readErr)
				}
				entries, err := os.ReadDir(filepath.Join(root, "reports"))
				if err != nil || len(entries) != 1 {
					t.Fatalf("wrote artifacts before refusing: %v, %v", entries, err)
				}
			})
		}
	}
}

func TestReviewChecksTreeAfterArtefacts(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ -f '" + root + "/out/review.md' ]; then\n  echo 'package tampered' > '" + root + "/tracked.go'\nfi\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	err = run([]string{"review", "--base", base, "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "source tree changed") {
		t.Fatalf("review = %v, want post-publication tree check", err)
	}
	states, err := filepath.Glob(filepath.Join(root, ".batuta/reviews/state/*.json"))
	if err != nil || len(states) != 0 {
		t.Fatalf("checkpoint published after mutation: %v, %v", states, err)
	}
}

func TestReviewSpecPath(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	reviewGit(t, root, "checkout", "-qb", "feature/spec")
	for _, location := range []string{"plans", "plans/done"} {
		dir := filepath.Join(root, ".batuta", location)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		plan := "# Plan — Spec\n**Goal:** Review\n**Status:** approved\n## Tasks\n- [ ] 1. Check — docs/low\n      Accept: criteria from " + location + "\n"
		if err := os.WriteFile(filepath.Join(dir, "delivery.md"), []byte(plan), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sawSpec := false
	restore := stubReviewRunner(t, func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
		prompt := command.Args[len(command.Args)-1]
		if strings.Contains(prompt, "<<<CRITERIA") {
			sawSpec = true
			if !strings.Contains(prompt, "criteria from plans/done") {
				t.Errorf("wrong spec: %s", prompt)
			}
			return publication.CommandResult{Stdout: []byte("<<<CRITERIA\n{\"id\":\"task-1.1\",\"status\":\"satisfied\",\"path\":\"change.go:3\"}\nCRITERIA>>>\n")}, nil
		}
		return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
	})
	defer restore()
	if err := run([]string{"review", "--base", base, "--spec", ".batuta/plans/done/delivery.md", "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !sawSpec {
		t.Fatal("no spec sweep")
	}
	onlyReviewStatePath(t, root)
}

func TestReviewAllowsUntrackedArtifactDirectory(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	restore := stubReviewSessions(t, nil, nil)
	defer restore()
	if err := run([]string{"review", "--base", base, "--out", "reports/new"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "reports/new/review.md")); err != nil {
		t.Fatal(err)
	}
}

func TestResumeDryRunLeavesNoLock(t *testing.T) {
	root, _ := reviewCommandRepo(t)
	skills := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skills, "adapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".batuta", "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	planText := "# Plan — Preview\n\n**Goal:** Preview a resumed delivery.\n**Status:** approved\n\n## Tasks\n- [x] 1. Build — backend/high\n      Scope: change.go\n      Accept: file exists → test -f change.go\n"
	if err := os.WriteFile(filepath.Join(root, routing.PlanPath("preview")), []byte(planText), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, root, "add", ".")
	reviewGit(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "prepare preview")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".batuta/journal/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loader, err := routing.NewPlanLoader(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := loader.LoadPlan("preview")
	if err != nil {
		t.Fatal(err)
	}
	head := reviewGit(t, root, "rev-parse", "HEAD")
	detail, err := json.Marshal(map[string]any{
		"slug": "preview", "plan_digest": plan.Set.Digest,
		"branch": reviewGit(t, root, "branch", "--show-current"), "head": head, "parallel": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := json.Marshal(routing.DeliveryGraph{
		Waves: []routing.DeliveryWave{}, Integrations: []routing.IntegrationOperation{}, Pauses: []routing.HumanPause{},
		Tasks: []routing.GraphTask{{
			TaskID: "task_1", Domain: routing.DomainBackend, Complexity: routing.ComplexityHigh,
			Dependencies: []string{}, Attempts: []routing.GraphTaskAttempt{},
			State: routing.GraphTaskIntegrated, IntegratedCommitSHA: head,
		}}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("preview", journal.Record{Kind: loop.KindOpened, Detail: detail, Graph: graph}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var stdout, stderr bytes.Buffer
		if err := run([]string{"loop", "--workspace", root, "--skills", skills, "--resume", "preview", "--dry-run"}, &stdout, &stderr); err != nil {
			t.Fatalf("resume preview: %v\n%s", err, &stderr)
		}
		if !strings.Contains(stdout.String(), "delivery  preview") {
			t.Fatalf("missing preview: %s", &stdout)
		}
		if _, err := os.Stat(filepath.Join(root, journal.Dir, "preview.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preview retained ownership: %v", err)
		}
	}
}

func TestReviewInterruptedReportsCancellation(t *testing.T) {
	root, _ := reviewCommandRepo(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	git := publication.GitClient{Executable: gitPath, Runner: publication.ExecRunner{}}
	baseline, err := git.WorktreeState(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = reviewSessionError(ctx, git, root, baseline, nil, context.Canceled)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "source tree changed") {
		t.Fatalf("interrupted review = %v", err)
	}
}

func TestReviewStateErrorIsNotMutation(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	stateErr := errors.New("git unavailable")
	sessionErr := errors.New("review executor failed")
	git := publication.GitClient{Executable: gitPath, Runner: mainReviewRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
		return publication.CommandResult{}, stateErr
	})}
	for _, failure := range []error{nil, sessionErr} {
		err := reviewSessionError(context.Background(), git, t.TempDir(), publication.WorktreeState{}, nil, failure)
		if !errors.Is(err, stateErr) || (failure != nil && !errors.Is(err, failure)) || strings.Contains(err.Error(), "source tree changed") {
			t.Fatalf("state failure = %v", err)
		}
	}
}

func writeReviewProofPlan(t *testing.T, root, accept string) string {
	t.Helper()
	path := filepath.Join(root, "spec.md")
	payload := "# Plan — Spec\n**Goal:** Review\n**Status:** approved\n## Tasks\n- [ ] 1. Check — docs/low\n      Accept: " + accept + "\n"
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReviewRefusesProofThatChangesTree(t *testing.T) {
	for _, tc := range []struct{ name, proof, path string }{
		{"tracked", "printf changed > tracked.go", "tracked.go"},
		{"already dirty", "printf changed > change.go", "change.go"},
		{"untracked", "printf changed > new.go", "worktree state changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, base := reviewCommandRepo(t)
			t.Chdir(root)
			spec := writeReviewProofPlan(t, root, "safe → true; mutates → "+tc.proof)
			restore := stubReviewRunner(t, func(context.Context, publication.Command) (publication.CommandResult, error) {
				t.Fatal("reviewer started after proof changed tree")
				return publication.CommandResult{}, nil
			})
			defer restore()
			err := run([]string{"review", "--base", base, "--spec", spec, "--out", "out"}, &bytes.Buffer{}, &bytes.Buffer{})
			want := "review: proof of task-1.2 changed the source tree: " + tc.path
			if err == nil || err.Error() != want {
				t.Fatalf("error=%v, want %q", err, want)
			}
		})
	}
}

func TestReviewWithOnlyProofRulesRunsNoSweep(t *testing.T) {
	for _, tc := range []struct {
		name, accept string
		statuses     []review.CriterionStatus
	}{
		{"passing", "first → test -f tracked.go; second → true", []review.CriterionStatus{review.CriterionSatisfied, review.CriterionSatisfied}},
		{"failing", "first → false; second → true", []review.CriterionStatus{review.CriterionViolated, review.CriterionSatisfied}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, base := reviewCommandRepo(t)
			t.Chdir(root)
			spec := writeReviewProofPlan(t, root, tc.accept)
			calls := 0
			restore := stubReviewRunner(t, func(_ context.Context, cmd publication.Command) (publication.CommandResult, error) {
				calls++
				if strings.Contains(cmd.Args[len(cmd.Args)-1], "<<<CRITERIA") {
					t.Fatal("all-proof plan started a spec sweep")
				}
				return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
			})
			defer restore()
			var stdout bytes.Buffer
			err := run([]string{"review", "--base", base, "--spec", spec, "--out", "out"}, &stdout, &bytes.Buffer{})
			if tc.name == "failing" {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 3 {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("reviewer calls=%d, want one cohort only", calls)
			}
			payload, readErr := os.ReadFile(filepath.Join(root, "out", "review.md"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(payload) != stdout.String() {
				t.Fatal("saved report differs from stdout")
			}
			previous := -1
			for i, status := range tc.statuses {
				want := fmt.Sprintf("| task-1.%d | %s | proof: ", i+1, status)
				position := strings.Index(stdout.String(), want)
				if position <= previous {
					t.Fatalf("missing or out-of-order %q in %s", want, stdout.String())
				}
				previous = position
			}
		})
	}
}

func TestReviewWithMixedSpecRules(t *testing.T) {
	root, base := reviewCommandRepo(t)
	t.Chdir(root)
	spec := writeReviewProofPlan(t, root, "first → true; manual criterion; last → false")
	var sessions []string
	restore := stubReviewRunner(t, func(_ context.Context, cmd publication.Command) (publication.CommandResult, error) {
		prompt := cmd.Args[len(cmd.Args)-1]
		if strings.Contains(prompt, "<<<CRITERIA") {
			sessions = append(sessions, "sweep")
			if strings.Contains(prompt, "[task-1.1]") || strings.Contains(prompt, "[task-1.3]") || !strings.Contains(prompt, "[task-1.2]") || strings.Contains(prompt, "Proof:") {
				t.Fatalf("sweep did not receive only arrow-less criterion: %s", prompt)
			}
			return publication.CommandResult{Stdout: []byte("<<<CRITERIA\n" + `{"id":"task-1.2","status":"satisfied","path":"change.go:3"}` + "\nCRITERIA>>>\n")}, nil
		}
		sessions = append(sessions, "cohort")
		return publication.CommandResult{Stdout: []byte("<<<FINDINGS\nFINDINGS>>>\n")}, nil
	})
	defer restore()
	var stdout bytes.Buffer
	err := run([]string{"review", "--base", base, "--spec", spec, "--out", "out"}, &stdout, &bytes.Buffer{})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("error=%v", err)
	}
	if !slices.Equal(sessions, []string{"cohort", "sweep"}) {
		t.Fatalf("sessions=%v", sessions)
	}
	previous := -1
	for _, want := range []string{
		"| task-1.1 | satisfied | proof: true exited 0 |",
		"| task-1.2 | satisfied | change.go:3 |",
		"| task-1.3 | violated | proof: false exited 1 |",
	} {
		position := strings.Index(stdout.String(), want)
		if position <= previous {
			t.Fatalf("missing or out-of-order %q in %s", want, stdout.String())
		}
		previous = position
	}
}

func loopSupervisionFixture(t *testing.T) string {
	t.Helper()
	root, skills := loopSupervisionFixtureWithoutAmbientSkills(t)
	t.Setenv("BATUTA_SKILLS", skills)
	return root
}

func loopSupervisionFixtureWithoutAmbientSkills(t *testing.T) (string, string) {
	t.Helper()
	root, skills := loopSupervisionIsolatedFixture(t)
	t.Setenv("BATUTA_SKILLS", "")
	t.Setenv("PATH", skills+string(os.PathListSeparator)+os.Getenv("PATH"))
	return root, skills
}

func loopSupervisionIsolatedFixture(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]string{
		".gitignore":            ".batuta/journal/\n.batuta/reviews/\n.batuta/runs/\n",
		".batuta/roadmap.md":    "# Roadmap — Delivery\n\n- [ ] 1. Demo → plans/demo.md\n",
		".batuta/profile.md":    "Stack: Go\nMethodology: TDD\nTest: true\nBuild: true\nExecution: sequential\nWorktree: off\nTemplate: templates/generic.md\n",
		".batuta/routing.md":    "| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| high | * | codex | review-model |\n",
		".batuta/plans/demo.md": "# Plan — Supervised\n\n**Goal:** Test review wiring.\n**Status:** approved\n\n## Tasks\n- [ ] 1. Add source — backend/low\n      Scope: source.txt\n      Accept: source exists → test -f source.txt\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reviewGit(t, root, "init", "-q")
	reviewGit(t, root, "config", "user.name", "Batuta Test")
	reviewGit(t, root, "config", "user.email", "batuta@example.test")
	reviewGit(t, root, "config", "commit.gpgsign", "false")
	reviewGit(t, root, "add", ".")
	reviewGit(t, root, "commit", "-qm", "plan")
	base := reviewGit(t, root, "rev-parse", "HEAD")
	plan, err := routing.ParsePlan("demo", []byte("# Plan — Supervised\n\n**Goal:** Test review wiring.\n**Status:** approved\n\n## Tasks\n- [ ] 1. Add source — backend/low\n      Scope: source.txt\n      Accept: source exists → test -f source.txt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("delivered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, root, "add", "source.txt")
	reviewGit(t, root, "commit", "-qm", "delivery")
	final := reviewGit(t, root, "rev-parse", "HEAD")
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := json.Marshal(map[string]any{"slug": "demo", "plan_path": ".batuta/plans/demo.md", "plan_digest": plan.Set.Digest, "head": base, "branch": reviewGit(t, root, "branch", "--show-current"), "supervision": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("supervised", journal.Record{Kind: loop.KindOpened, Detail: opened}); err != nil {
		t.Fatal(err)
	}
	terminal, err := json.Marshal(map[string]any{"state": loop.StateDone, "final_commit": final})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("supervised", journal.Record{Kind: loop.KindTerminal, Detail: terminal}); err != nil {
		t.Fatal(err)
	}
	skills := t.TempDir()
	for name, payload := range map[string]string{
		"adapters/codex.md":    "---\nname: codex\nrun: fake-reviewer {brief}\nreadonly: fake-reviewer {prompt}\nmodel_flags: --model {model}\navailable: fake-reviewer --version\nmodels: fake-reviewer models\nfinished: exit_code\n---\n",
		"templates/generic.md": "## Conventions for briefs\nKeep changes scoped.\n",
		"fake-reviewer":        "#!/bin/sh\nprintf '%s\\n' '<<<FINDINGS' 'FINDINGS>>>'\n",
	} {
		path := filepath.Join(skills, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o600)
		if name == "fake-reviewer" {
			mode = 0o700
		}
		if err := os.WriteFile(path, []byte(payload), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root, skills
}

func TestLoopSupervisionOnce(t *testing.T) {
	root, skills := loopSupervisionFixtureWithoutAmbientSkills(t)
	cursor := filepath.Join(root, "cursor.json")
	var stdout, stderr bytes.Buffer
	args := []string{"loop", "--workspace", root, "--skills", skills, "--supervise", "supervised", "--cursor", cursor, "--once"}
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"completed":true`) || !strings.Contains(stdout.String(), `"notification":"unconfigured"`) ||
		!strings.Contains(stdout.String(), `"review":{"outcome":"SHIP"`) || !strings.Contains(stdout.String(), `"state":"reported"`) {
		t.Fatalf("stdout=%s stderr=%s", &stdout, &stderr)
	}
	if _, err := os.Stat(cursor); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stdout.String(), `"id":"supervised:2"`) != 1 || !strings.Contains(stdout.String(), `"attempts":1`) {
		t.Fatalf("restart output=%s", &stdout)
	}
}

func TestLoopSupervisionRejectsAmbiguousFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--once"}, {"--cursor", "/tmp/cursor"}, {"--notify", "desktop"}, {"--policy", "/tmp/policy"},
		{"--supervise", "demo"},
		{"--supervise", "demo", "--cursor", "/tmp/cursor", "--dashboard"},
		{"--supervise", "demo", "--cursor", "/tmp/cursor", "--resume", "demo"},
		{"--supervise", "demo", "--cursor", "/tmp/cursor", "--dry-run"},
		{"--supervise", "demo", "--cursor", "/tmp/cursor", "--parallel", "1"},
		{"--supervise", "demo", "--cursor", "/tmp/cursor", "plan.md"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(append([]string{"loop"}, args...), &stdout, &stderr); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestLoopSupervisionLocalFileSink(t *testing.T) {
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("supervised", journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`)}); err != nil {
		t.Fatal(err)
	}
	sink := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"loop", "--workspace", root, "--supervise", "supervised", "--cursor", filepath.Join(root, "cursor.json"), "--notify", sink, "--once"}
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"notification":"acknowledged"`) || !strings.Contains(stdout.String(), `"pending":0`) {
		t.Fatalf("output=%s", &stdout)
	}
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(sink)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
}

func TestLoopSupervisionReviewEngineFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"review", "-h"}, &stdout, &stderr); err == nil {
		t.Fatal("expected help sentinel")
	}
	for _, flag := range []string{"-base string", "-spec string", "-full", "-out string"} {
		if !strings.Contains(stderr.String(), flag) {
			t.Fatalf("review engine lacks %s: %s", flag, &stderr)
		}
	}
	stdout.Reset()
	if err := runCapabilities(&stdout); err != nil {
		t.Fatal(err)
	}
	var caps capabilities
	if err := json.Unmarshal(stdout.Bytes(), &caps); err != nil || !slices.Contains(caps.Commands, "review") {
		t.Fatalf("capabilities: %s, %v", &stdout, err)
	}
}

func TestLoopSupervisionRoutedExecutionSettings(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	skills, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	fake := filepath.Join(state, "codex")
	worker := `#!/bin/sh
set -eu
case "$1" in
  --version) echo 'codex 1.0.0';;
  debug) echo '{"models":[{"slug":"chosen-model"}]}';;
  doctor|plugin) echo '{}';;
  run)
    test "$2" = chosen-model
    test "$3" = high
    cp "$4" "$BATUTA_SUPERVISION_CALLS/brief-$(cat "$BATUTA_SUPERVISION_CALLS/next").md"
    if grep -q '^The answer: ' "$4"; then
      echo 3 > "$BATUTA_SUPERVISION_CALLS/next"
      echo 'BATUTA-QUESTION: choose the final behavior'
    else
      echo 2 > "$BATUTA_SUPERVISION_CALLS/next"
      echo 'BATUTA-QUESTION: clarify the approved task ownership'
    fi
    ;;
  *) exit 91;;
esac
`
	plan := "# Plan — Supervised\n\n**Goal:** Test routed continuation.\n**Status:** approved\n\n## Tasks\n- [ ] 1. Add source — backend/high\n      Scope: source.txt\n      Accept: source exists → test -f source.txt\n"
	for name, payload := range map[string]string{
		filepath.Join(root, ".gitignore"):             ".batuta/journal/\n.batuta/worktrees/\n.batuta/logs/\n.batuta/asks/\n",
		filepath.Join(root, ".batuta/profile.md"):     "Stack: shell\nMethodology: TDD\nTest: true\nBuild: true\nExecution: sequential\nWorktree: always\nTemplate: templates/generic.md\n",
		filepath.Join(root, ".batuta/routing.md"):     "| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| high | * | codex | chosen-model |\n",
		filepath.Join(root, ".batuta/plans/demo.md"):  plan,
		filepath.Join(skills, "adapters/codex.md"):    fmt.Sprintf("---\nname: codex\nexecutable: %s\nrun: %s run {model_flags} \"{brief}\"\nrun_file: %s run {model_flags} \"{brief_file}\"\nmodel_flags: {model} {effort}\nreadonly: unused\navailable: codex --version\nmodels: codex debug models\nfinished: exit_code\nbrief_limit_lines: 1\n---\n", fake, fake, fake),
		filepath.Join(skills, "templates/generic.md"): "## Conventions for briefs\nKeep changes scoped.\n",
		filepath.Join(state, "next"):                  "1\n",
		fake:                                          worker,
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(fake, 0700); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(state, "git")); err != nil {
		t.Fatal(err)
	}
	// Inventory and execution both resolve only this owned executor.
	t.Setenv("PATH", state+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("BATUTA_SUPERVISION_CALLS", state)
	reviewGit(t, root, "init", "-q")
	reviewGit(t, root, "config", "commit.gpgsign", "false")
	reviewGit(t, root, "add", ".")
	reviewGit(t, root, "commit", "-qm", "plan")
	var stdout, stderr bytes.Buffer
	initial := []string{"loop", "--workspace", root, "--skills", skills, "--transport", "cli", "demo"}
	var exit *ExitError
	if err := run(initial, &stdout, &stderr); !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("initial waiting run: %v\nstdout=%s stderr=%s", err, &stdout, &stderr)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := store.List()
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries=%v err=%v", deliveries, err)
	}
	delivery := deliveries[0]
	initialRecords, err := store.Read(delivery)
	if err != nil || len(initialRecords) == 0 || !bytes.Contains(initialRecords[0].Detail, []byte(`"supervision":true`)) {
		t.Fatalf("normal loop omitted durable supervision: %v, %v", initialRecords, err)
	}
	if strings.Contains(stdout.String(), `"delivery":`) || stderr.Len() == 0 {
		t.Fatalf("normal output streams: stdout=%s stderr=%s", &stdout, &stderr)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(stderr.Bytes()), []byte("\n")) {
		if !json.Valid(line) {
			t.Fatalf("normal observation is not JSON: %s", line)
		}
	}
	cursor := filepath.Join(state, "cursor.json")
	observation, err := loop.ObserveSupervision(loop.SupervisionOptions{Workspace: root, Delivery: delivery, CursorPath: cursor})
	if err != nil {
		t.Fatal(err)
	}
	var event loop.SupervisionEvent
	for _, candidate := range observation.Events {
		if candidate.Kind == loop.KindQuestion {
			event = candidate
		}
	}
	if event.ID == "" || observation.TerminalState != loop.StateWaitingInput {
		t.Fatalf("missing real waiting question: %+v", observation)
	}
	policy := loop.SupervisionPolicy{Delivery: delivery, TaskID: event.TaskID, Execution: event.Execution, QuestionID: event.QuestionID, QuestionDigest: event.Evidence.Digest,
		Action: loop.SupervisionContinueApprovedTask, Ownership: "approved_task", MaxAttempts: 1,
		PlanEvidence: loop.SupervisionEvidence{Path: ".batuta/plans/demo.md", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(plan)))}}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(state, "policy.json")
	if err := os.WriteFile(policyPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	args := []string{"loop", "--workspace", root, "--supervise", delivery, "--cursor", cursor, "--once", "--policy", policyPath,
		"--skills", skills, "--transport", "cli", "--parallel", "1", "--task-timeout", "2m", "--test-timeout", "1m", "--max-waves", "1", "--keep-worktrees", "--max-limit-waits", "2", "--limit-horizon", "1h", "--limit-wait", "1m"}
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("continuation: %v\nstdout=%s stderr=%s", err, &stdout, &stderr)
	}
	var decision loop.SupervisionDecision
	for _, line := range bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n")) {
		var report struct {
			Decision *loop.SupervisionDecision `json:"decision"`
		}
		if err := json.Unmarshal(line, &report); err != nil {
			t.Fatalf("supervision stdout is not JSON: %s: %v", line, err)
		}
		if report.Decision != nil {
			decision = *report.Decision
		}
	}
	if decision.Outcome != "answered" || decision.Continuation != "resumed" || decision.RunState != loop.StateWaitingInput || decision.Attempts != 1 {
		t.Fatalf("continuation decision=%+v\nstdout=%s stderr=%s", decision, &stdout, &stderr)
	}
	if !strings.Contains(stderr.String(), "task_1 e2 → codex/chosen-model") || !strings.Contains(stderr.String(), "choose the final behavior") {
		t.Fatalf("resumed worker output missing from stderr: %s", &stderr)
	}
	brief, err := os.ReadFile(filepath.Join(state, "brief-2.md"))
	if err != nil || !strings.Contains(string(brief), "The answer: "+loop.SupervisionRoutineAnswer+"\n") {
		t.Fatalf("worker did not receive durable answer: %s, %v", brief, err)
	}
	records, err := store.Read(delivery)
	if err != nil {
		t.Fatal(err)
	}
	answers, starts := 0, 0
	for _, record := range records {
		switch record.Kind {
		case loop.KindAnswer:
			answers++
			var detail struct {
				Execution int
				Answer    string
			}
			if err := json.Unmarshal(record.Detail, &detail); err != nil || detail.Execution != event.Execution || detail.Answer != loop.SupervisionRoutineAnswer {
				t.Fatalf("bound answer=%s err=%v", record.Detail, err)
			}
		case loop.KindStarted:
			starts++
			var detail struct {
				Execution                  int
				Executor, Model, Reasoning string
			}
			if err := json.Unmarshal(record.Detail, &detail); err != nil || detail.Execution != starts || detail.Executor != "codex" || detail.Model != "chosen-model" || detail.Reasoning != "high" {
				t.Fatalf("routed worker=%s err=%v", record.Detail, err)
			}
		}
	}
	if answers != 1 || starts != 2 {
		t.Fatalf("answers=%d starts=%d", answers, starts)
	}
	var intent struct {
		Stage    string
		Settings struct {
			Skills, Transport, Verifier                              string
			Parallel, MaxWaves, MaxLimitWaits                        int
			TaskTimeout, TestTimeout, LimitWaitDefault, LimitHorizon time.Duration
			KeepWorktrees                                            bool
		}
	}
	readReviewJSON(t, filepath.Join(root, journal.Dir, fmt.Sprintf("%s.continuation-%d.json", delivery, event.Sequence)), &intent)
	settings := intent.Settings
	if intent.Stage != "resumed" || settings.Skills != skills || settings.Transport != "cli" || settings.Verifier != "cli" ||
		settings.Parallel != 1 || settings.MaxWaves != 1 || settings.MaxLimitWaits != 2 || settings.TaskTimeout != 2*time.Minute ||
		settings.TestTimeout != time.Minute || settings.LimitWaitDefault != time.Minute || settings.LimitHorizon != time.Hour || !settings.KeepWorktrees {
		t.Fatalf("resolved settings=%+v stage=%s", settings, intent.Stage)
	}
	before, err := os.ReadFile(store.Path(delivery))
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(store.Path(delivery))
	if err != nil || !bytes.Equal(before, after) || stderr.Len() != 0 {
		t.Fatalf("repeated CLI supervision executed again: %v stderr=%s", err, &stderr)
	}
	for _, action := range [][]string{{"--answer", "1", "explicit operator answer"}, {"--resume", delivery}} {
		stdout.Reset()
		stderr.Reset()
		normal := append([]string{"loop", "--workspace", root, "--skills", skills}, action...)
		if err := run(normal, &stdout, &stderr); !errors.As(err, &exit) || exit.Code != 3 {
			t.Fatalf("normal continuation: %v\nstdout=%s stderr=%s", err, &stdout, &stderr)
		}
		if stderr.Len() == 0 || strings.Contains(stdout.String(), `"delivery":`) {
			t.Fatalf("normal continuation streams: stdout=%s stderr=%s", &stdout, &stderr)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(stderr.Bytes()), []byte("\n")) {
			if !json.Valid(line) {
				t.Fatalf("continuation observation is not JSON: %s", line)
			}
		}
	}
}

func TestLoopRunExitPreservesIndependentFailures(t *testing.T) {
	failure := errors.New("observer or runtime failed")
	for _, tc := range []struct {
		name  string
		state string
		err   error
		code  int
	}{
		{"canceled-state", loop.StateCanceled, nil, 130},
		{"cancellation", loop.StateCanceled, context.Canceled, 130},
		{"wrapped-cancellation", loop.StateCanceled, fmt.Errorf("run: %w", context.Canceled), 130},
		{"joined-cancellations", loop.StateCanceled, errors.Join(context.Canceled, fmt.Errorf("observer: %w", context.Canceled)), 130},
		{"joined-failure", loop.StateCanceled, errors.Join(context.Canceled, failure), 0},
		{"nested-failure", loop.StateCanceled, fmt.Errorf("run: %w", errors.Join(failure, context.Canceled)), 0},
		{"failure", loop.StateCanceled, failure, 0},
		{"deadline", loop.StateCanceled, errors.Join(context.Canceled, context.DeadlineExceeded), 0},
		{"unfinished", "", context.Canceled, 0},
		{"done", loop.StateDone, nil, 0},
		{"final-review-cancellation", loop.StateDone, context.Canceled, 130},
		{"rediscovered-review-cancellation", loop.StateReviewBlocked, context.Canceled, 130},
		{"rediscovered-review-failure", loop.StateReviewBlocked, errors.Join(context.Canceled, failure), 0},
		{"final-review-joined-failure", loop.StateDone, errors.Join(context.Canceled, failure), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := loopRunExit(tc.state, tc.err)
			var exit *ExitError
			if tc.code != 0 {
				if !errors.As(err, &exit) || exit.Code != tc.code {
					t.Fatalf("exit=%v, want %d", err, tc.code)
				}
			} else if err != tc.err || errors.As(err, &exit) {
				t.Fatalf("error=%v, want original %v without exit mapping", err, tc.err)
			}
		})
	}
}

func TestLoopSupervisionCancellationWorker(t *testing.T) {
	ready := os.Getenv("BATUTA_CANCELLATION_READY")
	if ready == "" {
		return
	}
	if err := os.WriteFile(ready, []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	// The executor must terminate this process; the timer bounds a broken test.
	<-time.After(30 * time.Second)
	os.Exit(91)
}

func TestLoopSupervisionNormalCompletion(t *testing.T) {
	for _, mode := range []string{"resume", "roadmap", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			root, skills := loopSupervisionFixtureWithoutAmbientSkills(t)
			args := []string{"loop", "--workspace", root, "--skills", skills, "--resume", "supervised"}
			if mode == "roadmap" {
				args = []string{"loop", "--workspace", root, "--skills", skills, "--roadmap"}
			}
			if mode == "unavailable" {
				if err := os.Remove(filepath.Join(skills, "fake-reviewer")); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			err := run(args, &stdout, &stderr)
			if mode == "unavailable" {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 2 || exit.State != loop.StateReviewBlocked {
					t.Fatalf("missing required review: %v", err)
				}
			} else if err != nil {
				t.Fatalf("%v\nstdout=%s stderr=%s", err, &stdout, &stderr)
			}
			observation, err := loop.ObserveSupervision(loop.SupervisionOptions{Workspace: root, Delivery: "supervised"})
			if err != nil || observation.Review == nil {
				t.Fatalf("review missing: %+v, %v", observation, err)
			}
			if mode != "unavailable" && observation.Review.Outcome != "SHIP" {
				t.Fatalf("review: %+v", observation.Review)
			}
			if strings.Contains(stdout.String(), `"delivery":`) {
				t.Fatalf("observer mixed into worker stdout: %s", &stdout)
			}
			if stderr.Len() == 0 {
				t.Fatal("missing structured observation")
			}
			for _, line := range bytes.Split(bytes.TrimSpace(stderr.Bytes()), []byte("\n")) {
				if !json.Valid(line) {
					t.Fatalf("observer output is not JSON: %s", line)
				}
			}
		})
	}
}

func TestLoopSupervisionJudgment(t *testing.T) {
	for _, decision := range []string{"accept", "reject"} {
		t.Run(decision, func(t *testing.T) {
			root := loopSupervisionFixture(t)
			base := []string{"loop", "--workspace", root, "--supervise", "supervised"}
			var stdout, stderr bytes.Buffer
			err := run(append(slices.Clone(base), "--review-status"), &stdout, &stderr)
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 2 {
				t.Fatalf("pending status: %v", err)
			}
			var gate loop.SupervisionGate
			if err := json.Unmarshal(stdout.Bytes(), &gate); err != nil || gate.Cleared || gate.EvidenceDigest != "" {
				t.Fatalf("pending gate: %+v, %v", gate, err)
			}
			if _, err := os.Stat(filepath.Join(root, ".batuta/reviews/supervision", gate.ReviewID, "job.json")); !os.IsNotExist(err) {
				t.Fatalf("status launched review: %v", err)
			}
			stdout.Reset()
			if err := run(append(slices.Clone(base), "--cursor", filepath.Join(root, "cursor.json"), "--once"), &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			stdout.Reset()
			if err := run(append(slices.Clone(base), "--review-status"), &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(stdout.Bytes(), &gate); err != nil || !gate.Cleared || gate.EvidenceDigest == "" {
				t.Fatalf("reported gate: %+v, %v", gate, err)
			}
			args := append(slices.Clone(base), "--review-judgment", decision, "--review-id", gate.ReviewID, "--review-digest", gate.EvidenceDigest, "--rationale", "I reviewed the exact evidence.")
			for _, invalid := range [][]string{
				args[:len(args)-2],
				append(slices.Clone(args), "--resume", "supervised"),
				append(slices.Clone(args), "--policy", "missing.json"),
				append(slices.Clone(args), "--once"),
				append(slices.Clone(args), "--review-digest", "sha256:stale"),
			} {
				if err := run(invalid, &stdout, &stderr); err == nil {
					t.Fatalf("invalid judgment accepted: %v", invalid)
				}
			}
			before, err := os.ReadFile(filepath.Join(root, journal.Dir, "supervised.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			stdout.Reset()
			err = run(args, &stdout, &stderr)
			if decision == "accept" && err != nil || decision == "reject" && (!errors.As(err, &exit) || exit.Code != 2) {
				t.Fatalf("judgment: %v", err)
			}
			if err := json.Unmarshal(stdout.Bytes(), &gate); err != nil || gate.Cleared != (decision == "accept") {
				t.Fatalf("judged gate: %+v, %v", gate, err)
			}
			data, err := os.ReadFile(filepath.Join(root, ".batuta/reviews/supervision", gate.ReviewID, "progression.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved loop.SupervisionJudgment
			if err := json.Unmarshal(data, &saved); err != nil || saved.EvidenceDigest != gate.EvidenceDigest || saved.Decision != decision || saved.Rationale != "I reviewed the exact evidence." {
				t.Fatalf("saved: %+v, %v", saved, err)
			}
			after, err := os.ReadFile(filepath.Join(root, journal.Dir, "supervised.jsonl"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("judgment executed work: %v", err)
			}
		})
	}
}

func TestLoopSupervisionStandaloneHonorsJudgment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		reviewOutput string
		initialExit  int
		decision     string
		finalExit    int
	}{
		{name: "accepted adverse review", reviewOutput: "<<<FINDINGS\n{\"severity\":\"major\",\"kind\":\"defect\",\"file\":\"source.txt\",\"line\":1,\"premise\":\"The delivered value is wrong.\",\"path\":\"Read source.txt.\",\"verdict\":\"The value cannot ship.\",\"fix\":\"Correct the value.\"}\nFINDINGS>>>\n", initialExit: 2, decision: "accept", finalExit: 0},
		{name: "rejected SHIP review", reviewOutput: "<<<FINDINGS\nFINDINGS>>>\n", initialExit: 0, decision: "reject", finalExit: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, skills := loopSupervisionIsolatedFixture(t)
			if err := os.WriteFile(filepath.Join(skills, "fake-reviewer"), []byte("#!/bin/sh\nprintf '%s' '"+tc.reviewOutput+"'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			assertExit := func(err error, want int, output []byte) {
				t.Helper()
				var exit *exec.ExitError
				if want == 0 && err != nil || want != 0 && (!errors.As(err, &exit) || exit.ExitCode() != want) {
					t.Fatalf("exit=%v, want %d\n%s", err, want, output)
				}
			}

			observeArgs := []string{"--skills", skills, "--supervise", "supervised", "--cursor", filepath.Join(root, "cursor.json"), "--once"}
			output, err := loopSupervisionCommand(t, root, skills, observeArgs...).CombinedOutput()
			assertExit(err, tc.initialExit, output)

			status := loopSupervisionCommand(t, root, skills, "--supervise", "supervised", "--review-status")
			output, err = status.Output()
			assertExit(err, tc.initialExit, output)
			var gate loop.SupervisionGate
			if err := json.Unmarshal(output, &gate); err != nil || gate.EvidenceDigest == "" {
				t.Fatalf("gate=%+v, err=%v, output=%s", gate, err, output)
			}

			judgment := loopSupervisionCommand(t, root, skills, "--supervise", "supervised", "--review-judgment", tc.decision, "--review-id", gate.ReviewID, "--review-digest", gate.EvidenceDigest, "--rationale", "Reviewed the exact evidence.")
			output, err = judgment.Output()
			assertExit(err, tc.finalExit, output)

			output, err = loopSupervisionCommand(t, root, skills, observeArgs...).CombinedOutput()
			assertExit(err, tc.finalExit, output)
		})
	}
}

func TestLoopSupervisionNonexecutingCommands(t *testing.T) {
	for _, args := range [][]string{{"--dashboard"}, {"--roadmap", "--dry-run"}, {"--resume", "supervised", "--dry-run"}, {"--abandon", "supervised"}} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			root := loopSupervisionFixture(t)
			var stdout, stderr bytes.Buffer
			err := run(append([]string{"loop", "--workspace", root}, args...), &stdout, &stderr)
			if args[0] == "--abandon" || args[0] == "--resume" {
				if err == nil || !strings.Contains(err.Error(), "already ended: done") {
					t.Fatalf("abandon completed delivery: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, ".batuta/reviews")); !os.IsNotExist(err) {
				t.Fatalf("nonexecuting command launched review: %v", err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("nonexecuting command observed: %s", &stderr)
			}
		})
	}
}

func loopSupervisionCommand(t *testing.T, root, skills string, args ...string) *exec.Cmd {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, append([]string{"loop", "--workspace", root}, args...)...)
	cmd.Dir = t.TempDir()
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "BATUTA_SKILLS=") && !strings.HasPrefix(entry, "PATH=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(cmd.Env, "HOME="+home, "PATH="+skills+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cmd
}

func TestLoopSupervisionRelativeSkills(t *testing.T) {
	for _, mode := range []string{"resume", "standalone", "missing-explicit"} {
		t.Run(mode, func(t *testing.T) {
			root, skills := loopSupervisionIsolatedFixture(t)
			args := []string{"--resume", "supervised"}
			if mode == "standalone" {
				args = []string{"--supervise", "supervised", "--cursor", filepath.Join(root, "cursor.json"), "--once"}
			}
			cmd := loopSupervisionCommand(t, root, skills, args...)
			relative, err := filepath.Rel(cmd.Dir, skills)
			if err != nil {
				t.Fatal(err)
			}
			// A fallback installation deliberately disagrees with the selected adapter.
			fallback := t.TempDir()
			if err := os.MkdirAll(filepath.Join(fallback, "adapters"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd.Env = append(cmd.Env, "BATUTA_SKILLS="+fallback)
			if mode == "missing-explicit" {
				cmd.Env[len(cmd.Env)-1] = "BATUTA_SKILLS=" + skills
				relative = "missing-skills"
			}
			cmd.Args = append(cmd.Args, "--skills", relative)
			output, err := cmd.CombinedOutput()
			if mode == "missing-explicit" {
				if err == nil {
					t.Fatalf("invalid explicit skills silently fell back: %s", output)
				}
				return
			}
			if err != nil {
				t.Fatalf("relative skills: %v\n%s", err, output)
			}
			observation, err := loop.ObserveSupervision(loop.SupervisionOptions{Workspace: root, Delivery: "supervised"})
			if err != nil || observation.Review == nil || observation.Review.State != "reported" || observation.Review.Outcome != "SHIP" {
				t.Fatalf("selected adapter did not complete review: %+v %v\n%s", observation.Review, err, output)
			}
		})
	}
}

func TestLoopSupervisionIsolatedHome(t *testing.T) {
	for _, candidate := range []bool{false, true} {
		t.Run(fmt.Sprint(candidate), func(t *testing.T) {
			root := t.TempDir()
			if candidate {
				root, _ = loopSupervisionIsolatedFixture(t)
			} else {
				store, err := journal.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Append("supervised", journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`)}); err != nil {
					t.Fatal(err)
				}
			}
			sink := t.TempDir()
			args := []string{"--supervise", "supervised", "--cursor", filepath.Join(root, "cursor.json"), "--notify", sink, "--once"}
			for range 2 {
				cmd := loopSupervisionCommand(t, root, t.TempDir(), args...)
				output, err := cmd.CombinedOutput()
				if candidate {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 2 {
						t.Fatalf("required review not blocked: %v\n%s", err, output)
					}
					gate, err := loop.CheckSupervisionGate(context.Background(), loop.SupervisionOptions{Workspace: root, Delivery: "supervised"})
					if err != nil || !gate.Required || gate.Cleared {
						t.Fatalf("gate=%+v err=%v", gate, err)
					}
				} else if err != nil || !bytes.Contains(output, []byte(`"pending":0`)) {
					t.Fatalf("passive observation: %v\n%s", err, output)
				}
			}
			files, err := os.ReadDir(sink)
			if err != nil || len(files) == 0 || (!candidate && len(files) != 1) {
				t.Fatalf("notifications=%v err=%v", files, err)
			}
		})
	}
}

func TestLoopSupervisionRequiredReviewMissingIdentity(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("supervised", journal.Record{Kind: loop.KindOpened, Detail: json.RawMessage(`{"supervision":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("supervised", journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`)}); err != nil {
		t.Fatal(err)
	}

	cmd := loopSupervisionCommand(t, root, t.TempDir(), "--supervise", "supervised", "--cursor", filepath.Join(root, "cursor.json"), "--once")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("missing required review identity not blocked: %v\n%s", err, output)
	}
}

func TestLoopSupervisionStandaloneLegacyReview(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"adverse", "failed", "uncertain", "passive"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			root, skills := loopSupervisionIsolatedFixture(t)
			store, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			records, err := store.Read("supervised")
			if err != nil {
				t.Fatal(err)
			}
			records[0].Detail = bytes.Replace(records[0].Detail, []byte(`"supervision":true`), []byte(`"supervision":false`), 1)
			if scenario == "passive" {
				records = records[:1]
			}
			if err := os.Remove(store.Path("supervised")); err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				if _, err := store.Append("supervised", record); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "adverse" {
				payload := "#!/bin/sh\nprintf '%s\\n' '<<<FINDINGS' '{\"severity\":\"major\",\"kind\":\"defect\",\"file\":\"source.txt\",\"line\":1,\"premise\":\"Wrong value.\",\"path\":\"Read source.txt.\",\"verdict\":\"Cannot ship.\",\"fix\":\"Correct value.\"}' 'FINDINGS>>>'\n"
				if err := os.WriteFile(filepath.Join(skills, "fake-reviewer"), []byte(payload), 0700); err != nil {
					t.Fatal(err)
				}
			}
			observer := loop.SupervisionOptions{Workspace: root, Delivery: "supervised"}
			if scenario == "failed" || scenario == "uncertain" {
				// Seed a durable failed/uncertain launch through the public review API.
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				_, err := loop.RunSupervisionReview(ctx, observer, loop.SupervisionReviewOptions{Executable: "fake", Runner: mainReviewRunner(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
					if len(cmd.Args) == 1 {
						return publication.CommandResult{Stdout: []byte(`{"commands":["review"]}`)}, nil
					}
					if len(cmd.Args) == 2 {
						return publication.CommandResult{Stderr: []byte(" -base string\n -spec string\n -full\n -out string\n")}, nil
					}
					if scenario == "uncertain" {
						cancel()
						return publication.CommandResult{}, ctx.Err()
					}
					return publication.CommandResult{ExitCode: 1}, errors.New("review failed")
				})})
				if err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"--skills", skills, "--supervise", "supervised", "--cursor", filepath.Join(root, "cursor.json"), "--once"}
			output, err := loopSupervisionCommand(t, root, skills, args...).CombinedOutput()
			if scenario == "passive" {
				if err != nil {
					t.Fatalf("passive: %v\n%s", err, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 2 {
					t.Fatalf("legacy %s: %v, want exit 2\n%s", scenario, err, output)
				}
				observation, err := loop.ObserveSupervision(observer)
				if err != nil || observation.Review == nil {
					t.Fatalf("observation: %+v, %v", observation, err)
				}
				want := scenario
				if scenario == "adverse" {
					want = "reported"
				}
				if observation.Review.State != want {
					t.Fatalf("review=%+v, want %s", observation.Review, want)
				}
			}
			gate, err := loop.CheckSupervisionGate(context.Background(), observer)
			if err != nil || gate.Required || !gate.Cleared {
				t.Fatalf("legacy acquired durable gate: %+v, %v", gate, err)
			}
		})
	}
}
