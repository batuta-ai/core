//go:build linux || darwin

package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	if dir := os.Getenv("BATUTA_PROCESS_ANCESTOR_HELPER"); dir != "" {
		raw, rawErr := platformProcessScanner().Inside(context.Background(), dir)
		filtered, filteredErr := ProcessesInside(context.Background(), dir)
		result := struct {
			Self, Parent  int
			Raw, Filtered []Process
			Error         string
		}{Self: os.Getpid(), Parent: os.Getppid(), Raw: raw, Filtered: filtered}
		if rawErr != nil || filteredErr != nil {
			result.Error = fmt.Sprint(rawErr, "; ", filteredErr)
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		return
	}
	os.Exit(m.Run())
}

func TestProcessesInsideFindsChild(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	child := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	cmd.Dir = child
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	procs, err := ProcessesInside(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, proc := range procs {
		if proc.PID == cmd.Process.Pid {
			if proc.PPID != os.Getpid() || proc.Command != "sleep" || proc.Start.IsZero() || proc.Cwd != child {
				t.Fatalf("sleep process = %+v", proc)
			}
			return
		}
	}
	t.Fatalf("sleep PID %d missing from %v", cmd.Process.Pid, procs)
}

func TestProcessesInsideExcludesAncestors(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `"$1"; :`, "sh", os.Args[0])
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BATUTA_PROCESS_ANCESTOR_HELPER="+dir)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	err := cmd.Wait()
	if err != nil {
		t.Fatalf("helper: %v: %s", err, output.String())
	}
	var result struct {
		Self, Parent  int
		Raw, Filtered []Process
		Error         string
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode helper %q: %v", output.String(), err)
	}
	if result.Error != "" {
		t.Fatalf("helper scan error: %s", result.Error)
	}
	seen := map[int]bool{}
	for _, proc := range result.Raw {
		seen[proc.PID] = true
	}
	if !seen[result.Self] || !seen[result.Parent] {
		t.Fatalf("raw scanner missed helper or shell: self=%d parent=%d raw=%v", result.Self, result.Parent, result.Raw)
	}
	for _, proc := range result.Filtered {
		if proc.PID == result.Self || proc.PID == result.Parent {
			t.Fatalf("ancestor PID %d returned: %v", proc.PID, result.Filtered)
		}
	}
}
