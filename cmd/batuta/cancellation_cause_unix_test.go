//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/batuta-ai/core/loop"
)

func TestLoopRunExitPreservesSignalCancellationCause(t *testing.T) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGUSR2)
	defer stop()
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	cause := context.Cause(ctx)
	if !errors.Is(cause, context.Canceled) {
		t.Fatalf("signal cause = %v; want cancellation", cause)
	}
	failure := errors.New("independent cleanup failure")
	for _, tc := range []struct {
		name     string
		err      error
		canceled bool
	}{
		{"signal", cause, true},
		{"wrapped-signal", fmt.Errorf("journal: %w", cause), true},
		{"joined-cancellations", errors.Join(cause, context.Canceled), true},
		{"joined-failure", errors.Join(cause, failure), false},
		{"wrapped-joined-failure", fmt.Errorf("run: %w", errors.Join(cause, failure)), false},
		{"deadline", errors.Join(cause, context.DeadlineExceeded), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loopRunExit(loop.StateReviewBlocked, tc.err)
			var exit *ExitError
			if tc.canceled {
				if !errors.As(got, &exit) || exit.Code != 130 {
					t.Fatalf("signal cancellation exit = %v, want 130", got)
				}
			} else if got != tc.err || errors.As(got, &exit) {
				t.Fatalf("independent failure changed: got %v, want original %v", got, tc.err)
			}
		})
	}
}
