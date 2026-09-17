//go:build unix

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func TestLoopSupervisionFinalReviewSignalCancellation(t *testing.T) {
	root, skills := loopSupervisionIsolatedFixture(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	worker := fmt.Sprintf("#!/bin/sh\ncase \"$PWD\" in\n*/.batuta/reviews/supervision/*/source) exec '%s' -test.run=^TestLoopSupervisionCancellationWorker$;;\n*) echo 'TASK 1: DONE';;\nesac\n", binary)
	if err := os.WriteFile(filepath.Join(skills, "fake-reviewer"), []byte(worker), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.Path("supervised")); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(skills, "codex")
	executorScript := `#!/bin/sh
set -eu
case "$1" in
--version) echo 'codex 1.0.0';;
debug) echo '{"models":[{"slug":"review-model"}]}';;
doctor|plugin) echo '{}';;
run) echo implemented > source.txt; git add source.txt; git -c user.name=Test -c user.email=test@example.com -c commit.gpgsign=false commit -qm 'feat: implement source';;
*) exit 91;;
esac
`
	adapter := fmt.Sprintf("---\nname: codex\nexecutable: %s\nrun: %s run {model_flags} \"{brief}\"\nreadonly: fake-reviewer {prompt}\nmodel_flags: --model {model}\navailable: codex --version\nmodels: codex debug models\nfinished: exit_code\n---\n", fake, fake)
	for path, payload := range map[string]string{fake: executorScript, filepath.Join(skills, "adapters/codex.md"): adapter} {
		if err := os.WriteFile(path, []byte(payload), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// The CLI must execute and finalize this delivery before review starts.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".batuta/journal/\n.batuta/reviews/\n.batuta/runs/\n.batuta/worktrees/\n.batuta/logs/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, root, "add", ".gitignore")
	reviewGit(t, root, "commit", "-qm", "test: ignore execution artifacts")
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := loopSupervisionCommand(t, root, skills, "--skills", skills, "--transport", "cli", "--interval", "100ms", "demo")
	cmd.Env = append(cmd.Env, "BATUTA_CANCELLATION_READY="+ready)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("CLI exited before final reviewer started: %v\n%s", err, &output)
		case <-ticker.C:
			if _, err := os.Stat(ready); err != nil {
				continue
			}
			deliveries, err := store.List()
			if err != nil || len(deliveries) != 1 {
				t.Fatalf("deliveries=%v err=%v", deliveries, err)
			}
			delivery := deliveries[0]
			observer := loop.SupervisionOptions{Workspace: root, Delivery: delivery}
			before, err := loop.ObserveSupervision(observer)
			if err != nil || before.TerminalState != loop.StateDone || before.Review == nil || before.Review.State != "launching" {
				t.Fatalf("not in final review after done: %+v %v", before, err)
			}
			store, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, ".batuta/plans/done/demo.md")); err != nil {
				t.Fatalf("review preceded plan finalization: %v", err)
			}
			if source, err := os.ReadFile(filepath.Join(root, "source.txt")); err != nil || string(source) != "implemented\n" {
				t.Fatalf("review preceded implementation integration: %q %v", source, err)
			}
			journalBefore, err := os.ReadFile(store.Path(delivery))
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err = <-done
			pidBytes, readErr := os.ReadFile(ready)
			var reviewerPID int
			if _, scanErr := fmt.Sscan(string(pidBytes), &reviewerPID); readErr != nil || scanErr != nil || reviewerPID <= 0 {
				t.Fatalf("reviewer PID: %s %v %v", pidBytes, readErr, scanErr)
			}
			if err := syscall.Kill(reviewerPID, 0); !errors.Is(err, syscall.ESRCH) {
				t.Errorf("reviewer still alive after CLI exit: pid=%d err=%v", reviewerPID, err)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 130 {
				t.Errorf("final review signal exit=%v, want 130\n%s", err, &output)
			}
			after, err := loop.ObserveSupervision(observer)
			if err != nil || after.TerminalState != loop.StateDone || after.Review == nil || after.Review.State != "uncertain" || after.Review.Acceptance != "pending" {
				t.Fatalf("durable state lost: %+v %v", after, err)
			}
			journalAfter, err := os.ReadFile(store.Path(delivery))
			if err != nil || !bytes.Equal(journalBefore, journalAfter) {
				t.Fatalf("completed journal rewritten: %v", err)
			}
			replay := loopSupervisionCommand(t, root, skills, "--skills", skills, "--resume", delivery)
			replayOutput, err := replay.CombinedOutput()
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("uncertain review replay=%v\n%s", err, replayOutput)
			}
			last, err := loop.ObserveSupervision(observer)
			if err != nil || last.Review.ID != after.Review.ID || last.Review.Attempts != 1 || last.Review.State != "uncertain" {
				t.Fatalf("uncertain review retried: %+v %v", last.Review, err)
			}
			return
		}
	}
}
