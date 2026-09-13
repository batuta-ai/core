//go:build unix

package publication

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const reviewProcessMarker = "--review-process-helper"

func TestReviewProcessHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == reviewProcessMarker {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	mode, control := os.Args[index+1], os.Args[index+2]
	if mode == "child" {
		signal.Ignore(syscall.SIGTERM)
		if err := syscall.Mkfifo(control, 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		pipe, err := os.OpenFile(control, os.O_RDWR, 0)
		if err != nil {
			os.Exit(1)
		}
		defer pipe.Close()
		group, err := syscall.Getpgid(0)
		if err != nil {
			os.Exit(1)
		}
		fmt.Printf("ready %d %d\n", os.Getpid(), group)
		var stop [1]byte
		if _, err := pipe.Read(stop[:]); err != nil {
			os.Exit(1)
		}
		return
	}
	ctx := context.Background()
	if mode == "cooperative" {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, syscall.SIGTERM)
		defer stop()
	} else {
		signal.Ignore(syscall.SIGTERM)
	}
	fmt.Printf("outer %d\n", os.Getpid())
	_, err := (ExecRunner{}).Run(ctx, Command{
		Executable: os.Args[0], Args: []string{"-test.run=^TestReviewProcessHelper$", "--", reviewProcessMarker, "child", control},
		Observer: os.Stdout, StderrObserver: os.Stderr,
	})
	if !errors.Is(err, context.Canceled) {
		os.Exit(1)
	}
	fmt.Println("nested reaped")
}

type reviewReadyWriter struct {
	ready chan struct{}
	once  sync.Once
	data  string
}

func (w *reviewReadyWriter) Write(p []byte) (int, error) {
	w.data += string(p)
	if strings.Contains(w.data, "ready ") && strings.HasSuffix(w.data, "\n") {
		w.once.Do(func() { close(w.ready) })
	}
	return len(p), nil
}

func TestReviewProcessCancelNestedGroups(t *testing.T) {
	for _, mode := range []string{"cooperative", "uncooperative"} {
		t.Run(mode, func(t *testing.T) {
			// A FIFO lets the fixture release orphaned children without signaling a PID.
			dir, err := os.MkdirTemp("", "review-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			control := filepath.Join(dir, "control")
			defer func() {
				pipe, err := os.OpenFile(control, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err == nil {
					pipe.Write([]byte{1})
					pipe.Close()
				}
			}()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observer := &reviewReadyWriter{ready: make(chan struct{})}
			type outcome struct {
				result CommandResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := (ReviewRunner{}).Run(ctx, Command{
					Executable: binary, Args: []string{"-test.run=^TestReviewProcessHelper$", "--", reviewProcessMarker, mode, control}, Observer: observer,
				})
				done <- outcome{result, err}
			}()
			select {
			case <-observer.ready:
			case got := <-done:
				t.Fatalf("engine exited before nested readiness: %+v, %v", got.result, got.err)
			case <-time.After(10 * time.Second):
				t.Fatal("nested process did not become ready")
			}
			cancel()
			var got outcome
			select {
			case got = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("review cancellation exceeded bounded escalation")
			}
			if !errors.Is(got.err, context.Canceled) || !errors.Is(got.err, ErrReviewCleanupUnresolved) {
				t.Fatalf("lost cancellation or cleanup uncertainty: %+v, %v", got.result, got.err)
			}
			var outer, child, group int
			for _, line := range strings.Split(string(got.result.Stdout), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == "outer" {
					outer, _ = strconv.Atoi(fields[1])
				}
				if len(fields) == 3 && fields[0] == "ready" {
					child, _ = strconv.Atoi(fields[1])
					group, _ = strconv.Atoi(fields[2])
				}
			}
			if outer == 0 || child == 0 || group != child || group == outer {
				t.Fatalf("fixture did not create independent groups: %s", got.result.Stdout)
			}
			if mode == "cooperative" && !strings.Contains(string(got.result.Stdout), "nested reaped") {
				t.Fatalf("outer engine was killed before propagating cancellation: %s, %s", got.result.Stdout, got.result.Stderr)
			}
			if mode == "uncooperative" {
				pipe, err := os.OpenFile(control, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err != nil {
					t.Fatalf("fixture did not retain an unresolved descendant: %v", err)
				}
				pipe.Write([]byte{1})
				pipe.Close()
			}
		})
	}
}

func TestReviewProcessSignalAfterWaitDoesNotTargetReusedPID(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
	configureReviewProcess(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("signal did not use the retained process identity: %v", err)
	}
}
