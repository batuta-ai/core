package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/batuta-ai/core/loop"
)

func TestLoopRunExitPreservesErrorsWithUncanceledOrDeadlineContexts(t *testing.T) {
	t.Parallel()
	deadline, cancel := context.WithDeadline(context.Background(), time.Time{})
	defer cancel()
	if deadline.Err() != context.DeadlineExceeded {
		t.Fatalf("deadline context error = %v", deadline.Err())
	}
	failure := errors.New("independent failure")
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"uncanceled", context.Background(), failure},
		{"deadline", deadline, context.DeadlineExceeded},
		{"deadline-with-failure", deadline, errors.Join(context.Canceled, failure)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := loopRunExit(loop.StateReviewBlocked, tc.err, tc.ctx)
			var exit *ExitError
			if got != tc.err || errors.As(got, &exit) {
				t.Fatalf("independent failure changed: got %v, want original %v", got, tc.err)
			}
		})
	}
}
