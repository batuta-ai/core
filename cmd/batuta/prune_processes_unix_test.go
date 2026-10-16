//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// startInWorktree runs a long sleep whose working directory is dir and kills
// it when the test ends.
func startInWorktree(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// pruneRows splits prune output into worktree rows and process rows.
func pruneRows(t *testing.T, stdout string) (worktrees, processes [][]string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		fields := strings.Split(line, "\t")
		switch {
		case fields[0] == "process" && len(fields) == 5:
			processes = append(processes, fields)
		case len(fields) == 4:
			worktrees = append(worktrees, fields)
		default:
			t.Fatalf("unexpected row %q", line)
		}
	}
	return worktrees, processes
}

func TestLoopPruneProcesses(t *testing.T) {
	f := newPruneFixture(t)
	cmd := startInWorktree(t, f.orphan)
	pid := strconv.Itoa(cmd.Process.Pid)

	var stdout, stderr bytes.Buffer
	if err := run([]string{"loop", "--workspace", f.root, "--prune"}, &stdout, &stderr); err != nil {
		t.Fatalf("prune = %v\nstderr=%s", err, &stderr)
	}
	worktrees, processes := pruneRows(t, stdout.String())
	if len(worktrees) != 3 {
		t.Fatalf("worktree rows = %v, want 3 four-column rows", worktrees)
	}
	want := []string{"process", reconcileField(f.orphan), pid, "sleep", "-"}
	if len(processes) != 1 || strings.Join(processes[0], "\t") != strings.Join(want, "\t") {
		t.Fatalf("process rows = %v, want %v", processes, want)
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{"loop", "--workspace", f.root, "--prune", "--apply"}, &stdout, &stderr); err != nil {
		t.Fatalf("prune --apply = %v\nstderr=%s", err, &stderr)
	}
	_, processes = pruneRows(t, stdout.String())
	if len(processes) != 1 || processes[0][2] != pid {
		t.Fatalf("applied process rows = %v", processes)
	}
	if signal := processes[0][4]; signal != "TERM" && signal != "KILL" {
		t.Fatalf("signal = %q, want TERM or KILL", signal)
	}
	if _, err := os.Stat(f.orphan); !os.IsNotExist(err) {
		t.Fatalf("%s still exists: %v", f.orphan, err)
	}
}

func TestLoopPruneDryRunProcessesExit(t *testing.T) {
	f := newPruneFixture(t)
	startInWorktree(t, f.orphan)
	var stdout, stderr bytes.Buffer
	if err := run([]string{"loop", "--workspace", f.root, "--prune"}, &stdout, &stderr); err != nil {
		t.Fatalf("dry run that lists processes = %v, want exit 0\nstderr=%s", err, &stderr)
	}
	if _, processes := pruneRows(t, stdout.String()); len(processes) != 1 {
		t.Fatalf("process rows = %v, want 1", processes)
	}
}
