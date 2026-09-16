package loop

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestSupervisionReviewOwnershipCanceledWhileWaiting(t *testing.T) {
	for _, scenario := range []string{"cancel", "deadline", "release"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ownership")
			release, err := guardPresence(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if release != nil {
					release()
				}
			}()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					unlock, err := acquireSupervisionReviewOwnership(ctx, path)
					if unlock != nil {
						unlock()
					}
					done <- err
				}()
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("ownership returned before release or cancellation: %v", err)
				default:
				}
				want := context.DeadlineExceeded
				switch scenario {
				case "cancel":
					want = context.Canceled
					cancel()
				case "release":
					want = nil
					release()
					release = nil
				}
				if err := <-done; !errors.Is(err, want) {
					t.Fatalf("ownership error = %v, want %v", err, want)
				}
			})
		})
	}
}
