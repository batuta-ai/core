//go:build darwin

package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDarwinScanPartial(t *testing.T) {
	dir := tempDir(t)
	cmd := exec.Command("sleep", "30")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	script := filepath.Join(dir, "lsof")
	content := fmt.Sprintf("#!/bin/sh\nprintf 'p%d\\ncsleep\\nn%s\\n'\nexit 1\n", cmd.Process.Pid, dir)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	old := lsofPath
	lsofPath = script
	t.Cleanup(func() { lsofPath = old })
	procs, err := platformProcessScanner().Inside(context.Background(), dir)
	var partial *PartialScanError
	if !errors.As(err, &partial) || len(procs) != 1 || procs[0].PID != cmd.Process.Pid || procs[0].PPID != os.Getpid() || procs[0].Start.IsZero() {
		t.Fatalf("partial scan = %+v, %v", procs, err)
	}
}

func TestDarwinScanTimeout(t *testing.T) {
	dir := tempDir(t)
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "lsof")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \""+marker+"\"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := lsofPath
	lsofPath = script
	t.Cleanup(func() { lsofPath = old })
	started := time.Now()
	_, err := platformProcessScanner().Inside(context.Background(), dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("scan error = %v; want deadline exceeded", err)
	}
	if time.Since(started) > 7*time.Second {
		t.Fatalf("scan exceeded timeout: %s", time.Since(started))
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("lsof script did not run: %v", err)
	}
}
