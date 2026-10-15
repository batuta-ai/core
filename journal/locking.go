package journal

import (
	"context"
	"errors"
	"os"
	"time"
)

func lockJournal(file *os.File) (func() error, error) {
	return lockJournalContext(context.Background(), file)
}

func lockJournalContext(ctx context.Context, file *os.File) (func() error, error) {
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		unlock, busy, err := tryLockJournal(file)
		if err != nil {
			return nil, err
		}
		if !busy {
			if err := context.Cause(ctx); err != nil {
				return nil, errors.Join(err, unlock())
			}
			return unlock, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, context.Cause(ctx)
		case <-timer.C:
		}
	}
}
