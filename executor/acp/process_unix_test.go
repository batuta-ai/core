//go:build !windows

package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Every descendant has a driver-owned lifetime pipe and an independent ceiling.
// Cleanup tests cannot leave an indefinite orphan even when discovery is denied.
func TestOwnedProcessFixture(t *testing.T) {
	mode := os.Getenv("BATUTA_PROCESS_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "child" {
		fmt.Fprintln(os.Stdout, "ready")
		go func() { io.Copy(io.Discard, os.NewFile(3, "lifetime")); os.Exit(0) }()
		time.Sleep(15 * time.Second)
		os.Exit(0)
	}
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestOwnedProcessFixture$")
		child.Env = append(os.Environ(), "BATUTA_PROCESS_FIXTURE=child")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.ExtraFiles = []*os.File{os.NewFile(3, "lifetime")}
		child.Stderr = os.Stderr
		stdout, err := child.StdoutPipe()
		if err != nil || child.Start() != nil {
			os.Exit(2)
		}
		if line, _ := bufio.NewReader(stdout).ReadString('\n'); line != "ready\n" {
			os.Exit(3)
		}
		fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"child":%d}}`+"\n", child.Process.Pid)
		// Deliberately ignore stdin, including EOF and any cancel notification.
		time.Sleep(15 * time.Second)
		os.Exit(0)
	}
	os.Exit(4)
}

func TestProcessShutdownWithEscapedChildIsExplicitlyUnresolved(t *testing.T) {
	lifetime, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer lifetime.Close()
	defer release.Close()
	unrelated := exec.Command(os.Args[0], "-test.run=^TestOwnedProcessFixture$")
	unrelated.Env = append(os.Environ(), "BATUTA_PROCESS_FIXTURE=child")
	unrelated.ExtraFiles = []*os.File{lifetime}
	ready, err := unrelated.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { release.Close(); unrelated.Wait() }()
	if line, _ := bufio.NewReader(ready).ReadString('\n'); line != "ready\n" {
		t.Fatal("unrelated fixture did not start")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedProcessFixture$")
	cmd.Env = append(os.Environ(), "BATUTA_PROCESS_FIXTURE=parent")
	cmd.ExtraFiles = []*os.File{lifetime}
	process, err := StartProcess(context.Background(), cmd, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Shutdown()
	select {
	case notification := <-process.Connection.Notifications():
		var params struct{ Child int }
		if json.Unmarshal(notification.Params, &params) != nil || params.Child <= 0 {
			t.Fatalf("missing child identity: %s", notification.Params)
		}
		if group, err := syscall.Getpgid(params.Child); err != nil || group != params.Child || group == cmd.Process.Pid {
			t.Fatalf("child did not escape process group: %d / %v", group, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fixture did not start its escaped child")
	}
	done := make(chan error, 1)
	go func() { done <- process.Shutdown() }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCleanupUnresolved) {
			t.Fatalf("cleanup must not infer descendant exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown exceeded its bound")
	}
	select {
	case <-process.exited:
	default:
		t.Fatal("direct child was not reaped")
	}
	if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated process affected by cleanup: %v", err)
	}
	if err := process.Shutdown(); !errors.Is(err, ErrCleanupUnresolved) {
		t.Fatalf("repeated shutdown lost uncertainty: %v", err)
	}
}

func TestOwnedIdentitiesDoNotFollowReusedOrUnrelatedParents(t *testing.T) {
	root := processIdentity{pid: 10, started: "root"}
	child := processIdentity{pid: 11, started: "child"}
	owned := ownedProcesses{identities: map[int]processIdentity{10: root}}
	owned.observe([]processEntry{{identity: root}, {identity: child, parent: 10}, {identity: processIdentity{pid: 99, started: "unrelated"}, parent: 1}})
	if len(owned.identities) != 2 || owned.identities[11] != child {
		t.Fatalf("owned identities: %v", owned.identities)
	}
	owned.observe([]processEntry{
		{identity: processIdentity{pid: 10, started: "reused"}},
		{identity: processIdentity{pid: 12, started: "foreign"}, parent: 10},
		{identity: child, parent: 1}, // A changed process group/reparenting does not erase ownership.
	})
	if len(owned.identities) != 2 || owned.identities[10] != root || owned.identities[11] != child {
		t.Fatalf("reused PID acquired ownership: %v", owned.identities)
	}
}

func TestProcessDiscoveryIsBounded(t *testing.T) {
	root := processIdentity{pid: 1, started: "root"}
	owned := ownedProcesses{identities: map[int]processIdentity{1: root}}
	entries := []processEntry{{identity: root}}
	for i := 2; i < 1000; i++ {
		entries = append(entries, processEntry{identity: processIdentity{pid: i, started: strconv.Itoa(i)}, parent: 1})
	}
	owned.observe(entries)
	if len(owned.identities) > maxOwnedProcesses {
		t.Fatalf("unbounded identities: %d", len(owned.identities))
	}
}
