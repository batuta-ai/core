//go:build unix

package publication

import (
	"context"
	"errors"
	"fmt"
	"io"
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
		pipe, err := os.OpenFile(control, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			os.Exit(1)
		}
		defer pipe.Close()
		if err := syscall.SetNonblock(int(pipe.Fd()), false); err != nil {
			t.Fatal(err)
		}
		group, err := syscall.Getpgid(0)
		if err != nil {
			os.Exit(1)
		}
		fmt.Printf("ready %d %d\n", os.Getpid(), group)
		var stop [1]byte
		if _, err := pipe.Read(stop[:]); err != nil && !errors.Is(err, io.EOF) {
			os.Exit(1)
		}
		exited, err := os.OpenFile(control+".exited", os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			os.Exit(1)
		}
		defer exited.Close()
		if _, err := exited.Write([]byte{1}); err != nil {
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
			// Retain a writer before launch so cleanup can release even a late child.
			dir := t.TempDir()
			control := filepath.Join(dir, "control")
			if err := syscall.Mkfifo(control, 0600); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(control+".exited", 0600); err != nil {
				t.Fatal(err)
			}
			exited, err := os.OpenFile(control+".exited", os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer exited.Close()
			anchor, err := os.OpenFile(control, os.O_RDWR|syscall.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if anchor != nil {
					anchor.Close()
				}
			}()
			pipe, err := os.OpenFile(control, os.O_WRONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer pipe.Close()
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
			finished := false
			defer func() {
				cancel()
				_, releaseErr := pipe.Write([]byte{1})
				if releaseErr != nil && !errors.Is(releaseErr, syscall.EPIPE) {
					t.Errorf("release child fixture: %v", releaseErr)
				}
				if releaseErr == nil && anchor == nil && mode == "uncooperative" {
					acknowledged := make(chan error, 1)
					go func() {
						var ack [1]byte
						_, err := io.ReadFull(exited, ack[:])
						if err == nil && ack[0] != 1 {
							err = fmt.Errorf("unexpected release acknowledgment %v", ack)
						}
						acknowledged <- err
					}()
					select {
					case err := <-acknowledged:
						if err != nil {
							t.Errorf("child release: %v", err)
						}
					case <-time.After(10 * time.Second):
						t.Error("child did not acknowledge fixture release")
						if _, err := exited.Write([]byte{0}); err != nil {
							t.Errorf("release acknowledgment reader: %v", err)
						}
					}
				}
				if !finished {
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("review fixture did not finish during cleanup")
					}
				}
			}()
			select {
			case <-observer.ready:
				if err := anchor.Close(); err != nil {
					t.Fatal(err)
				}
				anchor = nil
			case got := <-done:
				finished = true
				t.Fatalf("engine exited before nested readiness: %+v, %v", got.result, got.err)
			case <-time.After(10 * time.Second):
				t.Fatal("nested process did not become ready")
			}
			cancel()
			var got outcome
			select {
			case got = <-done:
				finished = true
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
				if err := pipe.Close(); err != nil {
					t.Fatal(err)
				}
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
