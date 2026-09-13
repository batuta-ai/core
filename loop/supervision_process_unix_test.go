//go:build unix

package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
)

func TestSupervisionProcessHelper(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	dir, mode := args[0], args[1]
	if mode == "capabilities" {
		fmt.Println(`{"commands":["review"]}`)
		os.Exit(0)
	}
	if mode == "review" && args[2] == "-h" {
		fmt.Println("  -base string\n  -spec string\n  -full\n  -out string")
		os.Exit(0)
	}
	events, err := os.OpenFile(filepath.Join(dir, "events"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	gateName := "engine"
	if mode == "child" {
		gateName = "child"
	}
	gate, err := os.OpenFile(filepath.Join(dir, gateName), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	if mode == "child" {
		signal.Ignore(syscall.SIGTERM)
		events.Write([]byte{'r'})
	} else {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		_, err := (publication.ExecRunner{}).Run(ctx, publication.Command{
			Executable: os.Args[0], Args: []string{"-test.run=^TestSupervisionProcessHelper$", "--", dir, "child"},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("nested cancellation: %v", err)
		}
		events.Write([]byte{'c'})
	}
	var release [1]byte
	if _, err := gate.Read(release[:]); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisionProcessCancelHoldsOwnershipThroughCleanup(t *testing.T) {
	opts, _, _ := supervisionReviewFixture(t)
	dir := t.TempDir()
	for _, name := range []string{"events", "engine", "child"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	events, err := os.OpenFile(filepath.Join(dir, "events"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	for _, name := range []string{"engine", "child"} {
		t.Cleanup(func() {
			gate, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|syscall.O_NONBLOCK, 0)
			if err == nil {
				gate.Write([]byte{1})
				gate.Close()
			}
		})
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	engine := filepath.Join(dir, "review-engine")
	script := "#!/bin/sh\nexec " + quote(binary) + " -test.run=^TestSupervisionProcessHelper$ -- " + quote(dir) + " \"$@\"\n"
	if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		job *SupervisionReviewJob
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		job, err := RunSupervisionReview(ctx, opts, SupervisionReviewOptions{Executable: engine})
		done <- outcome{job, err}
	}()
	readEvent := func(want byte) {
		t.Helper()
		read := make(chan byte, 1)
		go func() {
			var value [1]byte
			if _, err := events.Read(value[:]); err == nil {
				read <- value[0]
			}
		}()
		select {
		case got := <-read:
			if got != want {
				t.Fatalf("event = %q, want %q", got, want)
			}
		case got := <-done:
			t.Fatalf("review exited before event %q: %+v, %v", want, got.job, got.err)
		case <-time.After(15 * time.Second):
			t.Fatalf("no process event %q", want)
		}
	}
	readEvent('r')
	cancel()
	readEvent('c')
	waiter, err := RunSupervisionReview(context.Background(), opts, SupervisionReviewOptions{Executable: engine, Timeout: time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || waiter != nil {
		t.Fatalf("ownership released during engine cleanup: %+v, %v", waiter, err)
	}
	gate, err := os.OpenFile(filepath.Join(dir, "engine"), os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	gate.Close()
	select {
	case got := <-done:
		if got.err != nil || got.job == nil || got.job.State != "uncertain" || got.job.Outcome != "cleanup_unresolved" || got.job.Acceptance != "pending" || got.job.Attempts != 1 {
			t.Fatalf("lost process uncertainty: %+v, %v", got.job, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("review cancellation did not finish")
	}
	// Removing the executable makes any accidental replay fail observably.
	if err := os.Remove(engine); err != nil {
		t.Fatal(err)
	}
	job, err := RunSupervisionReview(context.Background(), opts, SupervisionReviewOptions{Executable: engine})
	if err != nil || job.State != "uncertain" || job.Acceptance != "pending" || job.Attempts != 1 {
		t.Fatalf("replayed unresolved process: %+v, %v", job, err)
	}
}
