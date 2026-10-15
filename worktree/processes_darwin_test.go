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

func TestDarwinLongCommandStops(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(tempDir(t), "worktree")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// P_comm keeps 16 command bytes while lsof reports up to 31; the name is
	// longer than both, so the scanner and the stopper must read one field.
	// A copy of a system binary is killed by code signing, so the helper is a
	// copy of this test binary, whose ad-hoc signature survives the copy.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "a-dev-server-with-a-very-long-name")
	if err := os.WriteFile(binary, content, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "BATUTA_PROCESS_SLEEP_HELPER=1")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var found *Process
	deadline := time.Now().Add(5 * time.Second)
	for found == nil && time.Now().Before(deadline) {
		procs, err := ProcessesInside(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		for i := range procs {
			if procs[i].PID == cmd.Process.Pid {
				found = &procs[i]
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if found == nil {
		t.Fatalf("child PID %d not found", cmd.Process.Pid)
	}
	if len(found.Command) > 16 {
		t.Fatalf("scanned command %q is longer than P_comm", found.Command)
	}
	got, err := StopProcesses(context.Background(), dir, []Process{*found}, time.Minute)
	if err != nil || len(got) != 1 || !got[0].Confirmed() {
		t.Fatalf("StopProcesses() = %v, %v; want a confirmed stop", got, err)
	}
}
