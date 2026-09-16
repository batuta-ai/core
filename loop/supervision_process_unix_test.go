//go:build unix

package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	events, err := os.OpenFile(filepath.Join(dir, "events"), os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	gateName := "engine"
	if mode == "child" {
		gateName = "child"
	}
	gate, err := os.OpenFile(filepath.Join(dir, gateName), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	if err := syscall.SetNonblock(int(gate.Fd()), false); err != nil {
		t.Fatal(err)
	}
	if mode == "child" {
		signal.Ignore(syscall.SIGTERM)
		if _, err := events.Write([]byte{'r'}); err != nil {
			t.Fatal(err)
		}
	} else {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		_, err := (publication.ExecRunner{}).Run(ctx, publication.Command{
			Executable: os.Args[0], Args: []string{"-test.run=^TestSupervisionProcessHelper$", "--", dir, "child"},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("nested cancellation: %v", err)
		}
		if _, err := events.Write([]byte{'c'}); err != nil {
			t.Fatal(err)
		}
	}
	var release [1]byte
	n, err := gate.Read(release[:])
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if mode != "child" && n == 1 {
		if err := os.WriteFile(filepath.Join(dir, "engine-released"), release[:], 0600); err != nil {
			t.Fatal(err)
		}
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
	gates := make(map[string]*os.File)
	for _, name := range []string{"engine", "child"} {
		gate, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Close()
		gates[name] = gate
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
	finished := false
	defer func() {
		cancel()
		if _, err := events.Write([]byte{0}); err != nil {
			t.Errorf("release event reader: %v", err)
		}
		for name, gate := range gates {
			if _, err := gate.Write([]byte{1}); err != nil {
				t.Errorf("release %s fixture: %v", name, err)
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
			finished = true
			t.Fatalf("review exited before event %q: %+v, %v", want, got.job, got.err)
		case <-time.After(15 * time.Second):
			t.Fatalf("no process event %q", want)
		}
	}
	readEvent('r')
	job := supervisionObserve(t, opts).Review
	cancel()
	readEvent('c')
	select {
	case got := <-done:
		finished = true
		t.Fatalf("review exited before cleanup release: %+v, %v", got.job, got.err)
	default:
	}
	guard, err := os.OpenFile(filepath.Join(supervisionReviewDirectory(opts, *job), "ownership.guard"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	assertOwnership := func(held bool) {
		t.Helper()
		err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_UN); err != nil {
				t.Fatal(err)
			}
		}
		if held {
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				t.Fatalf("cleanup ownership did not contend on OS lock: %v", err)
			}
		} else if err != nil {
			t.Fatalf("ownership remained locked after review exit: %v", err)
		}
	}
	assertOwnership(true)
	if _, err := gates["engine"].Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		finished = true
		if got.err != nil || got.job == nil || got.job.State != "uncertain" || got.job.Outcome != "cleanup_unresolved" || got.job.Acceptance != "pending" || got.job.Attempts != 1 {
			t.Fatalf("lost process uncertainty: %+v, %v", got.job, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("review cancellation did not finish")
	}
	if released, err := os.ReadFile(filepath.Join(dir, "engine-released")); err != nil || string(released) != "\x01" {
		t.Fatalf("engine exited without consuming cleanup release: %q, %v", released, err)
	}
	assertOwnership(false)
	// Removing the executable makes any accidental replay fail observably.
	if err := os.Remove(engine); err != nil {
		t.Fatal(err)
	}
	job, err = RunSupervisionReview(context.Background(), opts, SupervisionReviewOptions{Executable: engine})
	if err != nil || job.State != "uncertain" || job.Acceptance != "pending" || job.Attempts != 1 {
		t.Fatalf("replayed unresolved process: %+v, %v", job, err)
	}
}
