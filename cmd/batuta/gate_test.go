package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/batuta-ai/core/publication"
)

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
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
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
