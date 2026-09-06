package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
)

func TestGateTests(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	repo := committedGitRepository(t, mustGit(t))
	tests := []struct {
		name       string
		args       []string
		wantOutput string
		wantCode   int
	}{
		{
			name:       "passing command",
			args:       []string{"gate", "tests", "--command", "printf 'ok\\n'", "--dir", repo},
			wantOutput: "{\"name\":\"tests\",\"pass\":true,\"signal\":\"`printf 'ok\\\\n'` passed\",\"detail\":\"ok\"}\n",
		},
		{
			name:       "failing command",
			args:       []string{"gate", "tests", "--command", "printf 'boom\\n' >&2; exit 3", "--dir", repo},
			wantOutput: "{\"name\":\"tests\",\"pass\":false,\"signal\":\"`printf 'boom\\\\n' \\u003e\\u00262; exit 3` exited 3\",\"detail\":\"boom\"}\n",
			wantCode:   2,
		},
		{
			name:     "missing command",
			args:     []string{"gate", "tests", "--dir", repo},
			wantCode: 1,
		},
		{
			name:     "empty command",
			args:     []string{"gate", "tests", "--command", "  ", "--dir", repo},
			wantCode: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(tt.args, &stdout, &stderr)
			if tt.wantCode == 0 && err != nil {
				t.Fatalf("run(%v) error = %v; stderr = %q", tt.args, err, stderr.String())
			}
			if tt.wantCode == 1 && err == nil {
				t.Fatalf("run(%v) succeeded, want usage error", tt.args)
			}
			if tt.wantCode == 2 {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 2 || exit.State != "tests failed" {
					t.Fatalf("run(%v) error = %v, want tests failed exit 2", tt.args, err)
				}
			}
			if got := stdout.String(); got != tt.wantOutput {
				t.Fatalf("run(%v) stdout = %q, want %q", tt.args, got, tt.wantOutput)
			}
		})
	}
}

func TestGateTestsTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	repo := committedGitRepository(t, mustGit(t))
	started := time.Now()
	var stdout, stderr bytes.Buffer
	err := run([]string{"gate", "tests", "--command", "sleep 5", "--timeout", "50ms", "--dir", repo}, &stdout, &stderr)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("gate tests timeout error = %v, want exit 2; stderr = %q", err, stderr.String())
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("gate tests timeout took %s, want under 2s", elapsed)
	}
	var verdict struct {
		Pass   bool   `json:"pass"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &verdict); err != nil {
		t.Fatalf("gate tests timeout output is not JSON: %v\n%s", err, stdout.String())
	}
	if verdict.Pass || !strings.Contains(verdict.Detail, "timed out after 50ms") {
		t.Fatalf("gate tests timeout verdict = %+v", verdict)
	}
}

func TestGateScope(t *testing.T) {
	git := mustGit(t)
	tests := []struct {
		name       string
		prepare    func(t *testing.T, repo string)
		scope      string
		wantOutput string
		wantCode   int
	}{
		{
			name: "inside scope with managed state",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(repo, "cmd", "batuta"), 0o755); err != nil {
					t.Fatal(err)
				}
				for path, content := range map[string]string{
					"cmd/batuta/gate.go": "package main\n",
					"WORK.md":            "managed\n",
				} {
					if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			},
			scope:      "cmd/batuta/gate.go, docs",
			wantOutput: `{"name":"scope","pass":true,"signal":"within Scope; managed state also touched: WORK.md","outside":[],"managed":["WORK.md"]}` + "\n",
		},
		{
			name: "outside scope",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, "outside.txt"), []byte("outside\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			scope:      "cmd/batuta",
			wantOutput: `{"name":"scope","pass":false,"signal":"1 path(s) outside Scope","outside":["outside.txt"],"managed":[]}` + "\n",
			wantCode:   2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := committedGitRepository(t, git)
			base := strings.TrimSpace(runGit(t, git, repo, "rev-parse", "HEAD"))
			tt.prepare(t, repo)

			var stdout, stderr bytes.Buffer
			err := run([]string{"gate", "scope", "--base", base, "--scope", tt.scope, "--dir", repo}, &stdout, &stderr)
			if tt.wantCode == 0 && err != nil {
				t.Fatalf("gate scope error = %v; stderr = %q", err, stderr.String())
			}
			if tt.wantCode == 2 {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 2 || exit.State != "scope failed" {
					t.Fatalf("gate scope error = %v, want scope failed exit 2", err)
				}
			}
			if got := stdout.String(); got != tt.wantOutput {
				t.Fatalf("gate scope stdout = %q, want %q", got, tt.wantOutput)
			}
		})
	}
}

func TestGateScopeResolvesRefs(t *testing.T) {
	git := mustGit(t)
	repo := committedGitRepository(t, git)
	runGit(t, git, repo, "tag", "scope-base")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"gate", "scope", "--base", "scope-base", "--scope", "tracked.txt", "--dir", repo}, &stdout, &stderr); err != nil {
		t.Fatalf("gate scope with ref error = %v; stderr = %q", err, stderr.String())
	}
	want := `{"name":"scope","pass":true,"signal":"within Scope","outside":[],"managed":[]}` + "\n"
	if got := stdout.String(); got != want {
		t.Fatalf("gate scope with ref stdout = %q, want %q", got, want)
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{"gate", "scope", "--base", "unknown-ref", "--scope", "tracked.txt", "--dir", repo}, &stdout, &stderr)
	if err == nil {
		t.Fatal("gate scope with unknown ref succeeded, want runtime error")
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		t.Fatalf("gate scope with unknown ref error = %v, want ordinary exit 1 error", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("gate scope with unknown ref stdout = %q, want empty", stdout.String())
	}
}

func TestGateScopeRequiresScopeFlag(t *testing.T) {
	repo := committedGitRepository(t, mustGit(t))
	var stdout, stderr bytes.Buffer
	if err := run([]string{"gate", "scope", "--base", "HEAD", "--dir", repo}, &stdout, &stderr); err == nil {
		t.Fatal("gate scope without --scope succeeded, want usage error")
	}
	if stdout.Len() != 0 {
		t.Fatalf("gate scope without --scope stdout = %q, want empty", stdout.String())
	}
}

func mustGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	return git
}

func TestGateTree(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	repo := committedGitRepository(t, git)

	var snapshotOut, stderr bytes.Buffer
	if err := run([]string{"gate", "tree", "--snapshot", "--dir", repo}, &snapshotOut, &stderr); err != nil {
		t.Fatalf("gate tree --snapshot error = %v; stderr = %q", err, stderr.String())
	}
	if strings.Count(snapshotOut.String(), "\n") != 1 || !strings.HasSuffix(snapshotOut.String(), "\n") {
		t.Fatalf("snapshot output = %q, want one newline-terminated line", snapshotOut.String())
	}
	var snapshot publication.WorktreeState
	if err := json.Unmarshal(snapshotOut.Bytes(), &snapshot); err != nil {
		t.Fatalf("snapshot output is not JSON: %v\n%s", err, snapshotOut.String())
	}
	if snapshot.HeadSHA == "" || snapshot.PorcelainSHA256 == "" || snapshot.ContentSHA256 == "" {
		t.Fatalf("snapshot = %+v, want all signature fields", snapshot)
	}

	tests := []struct {
		name    string
		prepare func(t *testing.T)
		want    string
	}{
		{
			name: "unchanged",
			want: `{"name":"tree","pass":true,"changed":false,"signal":"silent: the session wrote nothing"}` + "\n",
		},
		{
			name: "untracked file",
			prepare: func(t *testing.T) {
				if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: `{"name":"tree","pass":true,"changed":true,"signal":"the session wrote to the tree"}` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.prepare != nil {
				tt.prepare(t)
			}
			var stdout, stderr bytes.Buffer
			err := run([]string{"gate", "tree", "--before", strings.TrimSpace(snapshotOut.String()), "--dir", repo}, &stdout, &stderr)
			if err != nil {
				t.Fatalf("gate tree --before error = %v; stderr = %q", err, stderr.String())
			}
			if got := stdout.String(); got != tt.want {
				t.Fatalf("gate tree output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGateTreeRejectsInvalidInput(t *testing.T) {
	nonRepo := t.TempDir()
	tests := []struct {
		name string
		args []string
	}{
		{name: "malformed before", args: []string{"gate", "tree", "--before", "{"}},
		{name: "not a repository", args: []string{"gate", "tree", "--snapshot", "--dir", nonRepo}},
		{name: "both forms", args: []string{"gate", "tree", "--snapshot", "--before", `{}`}},
		{name: "neither form", args: []string{"gate", "tree"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if err := run(tt.args, &stdout, &stderr); err == nil {
				t.Fatalf("run(%v) succeeded, want error", tt.args)
			}
			if stdout.Len() != 0 {
				t.Fatalf("run(%v) stdout = %q, want empty", tt.args, stdout.String())
			}
		})
	}
}

func TestRunGateDispatch(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing gate", args: []string{"gate"}},
		{name: "unknown gate", args: []string{"gate", "bogus"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			err := run(tt.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "tree") {
				t.Fatalf("run(%v) error = %v, want error naming tree", tt.args, err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("run(%v) stdout = %q, want empty", tt.args, stdout.String())
			}
		})
	}
}

func TestGateIsNotAdvertisedByCapabilities(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"capabilities"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(capabilities) error = %v", err)
	}
	var got capabilities
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("capabilities is not JSON: %v", err)
	}
	if slices.Contains(got.Commands, "gate") {
		t.Fatalf("capabilities.commands = %v, must not advertise gate yet", got.Commands)
	}
}

func committedGitRepository(t *testing.T, git string) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"init", "-q"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
		{"config", "gc.autoDetach", "false"},
		{"config", "maintenance.auto", "false"},
	}
	for _, args := range commands {
		if out, err := exec.Command(git, append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-qm", "initial"}} {
		if out, err := exec.Command(git, append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func runGit(t *testing.T, git, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command(git, append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
