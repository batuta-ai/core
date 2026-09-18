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
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

// Every fixture has a driver-owned lifetime pipe and an independent ceiling.
// Cleanup tests cannot leave an indefinite orphan even when discovery is denied.
func TestOwnedProcessFixture(t *testing.T) {
	mode := os.Getenv("BATUTA_PROCESS_FIXTURE")
	if mode == "" {
		return
	}
	go func() { io.Copy(io.Discard, os.NewFile(3, "lifetime")); os.Exit(0) }()
	time.AfterFunc(15*time.Second, func() { os.Exit(0) })
	if mode == "child" || mode == "stubborn-child" {
		if mode == "stubborn-child" {
			signal.Ignore(syscall.SIGTERM)
		}
		fmt.Fprintln(os.Stdout, "ready")
		select {}
	}
	if mode == "normal" || mode == "eof" {
		fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","method":"session/update","params":{}}`)
		if mode == "normal" {
			var event [1]byte
			os.NewFile(4, "control").Read(event[:])
		} else {
			io.Copy(io.Discard, os.Stdin)
		}
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestOwnedProcessFixture$")
	childMode := "child"
	if mode == "kill" {
		childMode = "stubborn-child"
	}
	child.Env = append(os.Environ(), "BATUTA_PROCESS_FIXTURE="+childMode, "GORACE=atexit_sleep_ms=0")
	if mode == "escape" {
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	child.ExtraFiles = []*os.File{os.NewFile(3, "lifetime")}
	child.Stderr = os.Stderr
	stdout, err := child.StdoutPipe()
	if err != nil || child.Start() != nil {
		os.Exit(2)
	}
	if line, _ := bufio.NewReader(stdout).ReadString('\n'); line != "ready\n" {
		os.Exit(3)
	}
	// In the TERM case the root survives until it has reaped the child.
	signal.Ignore(syscall.SIGTERM)
	fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"child":%d}}`+"\n", child.Process.Pid)
	if mode == "root-first" {
		var event [1]byte
		os.NewFile(4, "control").Read(event[:])
		os.Exit(0)
	}
	child.Wait()
	os.Exit(0)
}

func processFixture(t *testing.T, mode string) (*exec.Cmd, *os.File) {
	t.Helper()
	lifetime, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	control, event, err := os.Pipe()
	if err != nil {
		lifetime.Close()
		release.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { release.Close(); lifetime.Close(); control.Close(); event.Close() })
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedProcessFixture$")
	cmd.Env = append(os.Environ(), "BATUTA_PROCESS_FIXTURE="+mode, "GORACE=atexit_sleep_ms=0")
	cmd.ExtraFiles = []*os.File{lifetime, control}
	return cmd, event
}

func TestProcessManagedGroupShutdown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		rootFirst  bool
		unresolved bool
	}{
		{name: "normal", rootFirst: true},
		{name: "eof"},
		{name: "term"},
		{name: "kill"},
		{name: "root-first", rootFirst: true},
		{name: "escape", unresolved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			unrelated, _ := processFixture(t, "child")
			ready, err := unrelated.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { unrelated.Process.Kill(); unrelated.Wait() })
			if line, _ := bufio.NewReader(ready).ReadString('\n'); line != "ready\n" {
				t.Fatal("unrelated fixture did not start")
			}
			cmd, event := processFixture(t, tc.name)
			process, err := StartProcess(context.Background(), cmd, Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { process.Shutdown() })
			var child int
			select {
			case notification := <-process.Connection.Notifications():
				var params struct{ Child int }
				if err := json.Unmarshal(notification.Params, &params); err != nil {
					t.Fatal(err)
				}
				child = params.Child
			case <-time.After(5 * time.Second):
				t.Fatal("fixture did not become ready")
			}
			if tc.unresolved {
				if group, err := syscall.Getpgid(child); err != nil || group != child || group == cmd.Process.Pid {
					t.Fatalf("child did not escape process group: %d / %v", group, err)
				}
			}
			if tc.rootFirst {
				if _, err := event.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-process.exited:
				case <-time.After(5 * time.Second):
					t.Fatal("root did not exit before shutdown")
				}
			}
			results := make(chan error, 8)
			var calls sync.WaitGroup
			for range cap(results) {
				calls.Go(func() { results <- process.Shutdown() })
			}
			done := make(chan struct{})
			go func() { calls.Wait(); close(results); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown exceeded its bound")
			}
			select {
			case <-process.exited:
			default:
				t.Fatal("direct child was not reaped")
			}
			if err := syscall.Kill(-cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("managed group still exists: %v", err)
			}
			if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unrelated process affected by cleanup: %v", err)
			}
			if tc.unresolved {
				if err := syscall.Kill(child, 0); err != nil {
					t.Fatalf("escaped child unexpectedly signaled: %v", err)
				}
			}
			if tc.name == "normal" || tc.name == "eof" || tc.name == "term" {
				if !cmd.ProcessState.Success() {
					t.Fatalf("cooperative root did not exit successfully: %v", cmd.ProcessState)
				}
			}
			if tc.name == "kill" {
				status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Fatalf("stubborn group did not require KILL: %v", cmd.ProcessState)
				}
			}
			for err := range results {
				if tc.unresolved && !errors.Is(err, ErrCleanupUnresolved) || !tc.unresolved && err != nil {
					t.Fatalf("shutdown: %v (want unresolved: %v)", err, tc.unresolved)
				}
			}
			if err := process.Shutdown(); tc.unresolved && !errors.Is(err, ErrCleanupUnresolved) || !tc.unresolved && err != nil {
				t.Fatalf("repeated shutdown: %v", err)
			}

		})
	}
}

func TestOwnedIdentitiesDoNotFollowReusedOrUnrelatedParents(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	root := processIdentity{pid: 1, started: "root"}
	owned := ownedProcesses{identities: map[int]processIdentity{1: root}}
	entries := []processEntry{{identity: root}}
	for i := 2; i < 1000; i++ {
		entries = append(entries, processEntry{identity: processIdentity{pid: i, started: strconv.Itoa(i)}, parent: 1})
	}
	owned.observe(entries)
	if len(owned.identities) > maxOwnedProcesses || !owned.uncertain {
		t.Fatalf("unbounded identities: %d", len(owned.identities))
	}
}

func TestOwnedResolutionPreservesEvidence(t *testing.T) {
	t.Parallel()
	root := processIdentity{pid: 10, started: "root"}
	child := processIdentity{pid: 11, started: "child"}
	for _, tc := range []struct {
		name      string
		entries   []processEntry
		uncertain bool
		resolved  bool
	}{
		{name: "drained", resolved: true},
		{name: "discovery-failed", uncertain: true},
		{name: "escaped-survivor", entries: []processEntry{{identity: child, parent: 1, group: 11}}},
		{name: "reused-pid", entries: []processEntry{{identity: processIdentity{pid: 11, started: "new"}, group: 11}}, resolved: true},
		{name: "unrelated", entries: []processEntry{{identity: processIdentity{pid: 99, started: "other"}, group: 99}}, resolved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			owned := ownedProcesses{identities: map[int]processIdentity{10: root}, uncertain: tc.uncertain}
			owned.observe([]processEntry{{identity: root, group: 10}, {identity: child, parent: 10, group: 10}})
			owned.observe(tc.entries)
			if got := owned.resolved(10, tc.entries); got != tc.resolved {
				t.Fatalf("resolved = %v, want %v", got, tc.resolved)
			}
		})
	}
}

func TestProcessGroupDrainRequiresExitAndReaping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		alive   bool
		reaped  bool
		drained bool
		wantErr bool
	}{
		{name: "active-group", alive: true, reaped: true},
		{name: "reaping-pending", wantErr: true},
		{name: "drained", reaped: true, drained: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd, _ := processFixture(t, "child")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			ready, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
			if line, _ := bufio.NewReader(ready).ReadString('\n'); line != "ready\n" {
				t.Fatal("fixture did not become ready")
			}
			if !tc.alive {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				cmd.Wait()
			}
			exited := make(chan struct{})
			if tc.reaped {
				close(exited)
			}
			grace := time.Duration(0)
			if tc.drained {
				grace = time.Second
			}
			drained, err := waitProcessGroup(cmd.Process.Pid, exited, grace)
			if drained != tc.drained || (err != nil) != tc.wantErr {
				t.Fatalf("drain = %v / %v, want %v / error %v", drained, err, tc.drained, tc.wantErr)
			}
		})
	}
}

func TestProcessGroupStatusPermissionDenied(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		next      error
		reapAfter time.Duration
		drained   bool
		wantErr   error
		elapsed   time.Duration
	}{
		{name: "transient-before-reaping", next: syscall.ESRCH, reapAfter: 30 * time.Millisecond, drained: true, elapsed: 30 * time.Millisecond},
		{name: "transient-after-reaping", next: syscall.ESRCH, reapAfter: 10 * time.Millisecond, drained: true, elapsed: 20 * time.Millisecond},
		{name: "persistent-with-reaping", next: syscall.EPERM, reapAfter: 30 * time.Millisecond, elapsed: 100 * time.Millisecond},
		{name: "persistent-without-reaping", next: syscall.EPERM, elapsed: 100 * time.Millisecond},
		{name: "active-after-denial", reapAfter: 30 * time.Millisecond, elapsed: 100 * time.Millisecond},
		{name: "disappeared-without-reaping", next: syscall.ESRCH, wantErr: ErrCleanupUnresolved, elapsed: 100 * time.Millisecond},
		{name: "other-error-after-denial", next: syscall.EINVAL, wantErr: syscall.EINVAL, elapsed: 20 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				exited := make(chan struct{})
				if tc.reapAfter != 0 {
					time.AfterFunc(tc.reapAfter, func() { close(exited) })
				}
				disappeared := false
				probe := func() error {
					if disappeared {
						t.Fatal("probed group ID after observing disappearance")
					}
					if time.Since(start) < 20*time.Millisecond {
						return syscall.EPERM
					}
					disappeared = errors.Is(tc.next, syscall.ESRCH)
					return tc.next
				}
				drained, err := waitProcessGroupStatus(probe, exited, 100*time.Millisecond)
				if drained != tc.drained || !errors.Is(err, tc.wantErr) {
					t.Fatalf("drain = %v / %v, want %v / %v", drained, err, tc.drained, tc.wantErr)
				}
				if elapsed := time.Since(start); elapsed != tc.elapsed {
					t.Fatalf("returned after %v, want %v", elapsed, tc.elapsed)
				}
			})
		})
	}
}
