package loop

import (
	"context"
	"errors"
	"os"
	"time"
)

// Reviews hold ownership through engine execution; waiting must consume the
// review deadline without changing the blocking guards used by presence.
func acquireSupervisionReviewOwnership(ctx context.Context, path string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path+".guard", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, file.Close())
		}
		locked, err := tryLockExclusive(file)
		if err != nil {
			return nil, errors.Join(err, file.Close())
		}
		if locked {
			// Preserve the shared inode, including on canceled acquisition.
			release := func() { unlockFile(file); _ = file.Close() }
			if err := ctx.Err(); err != nil {
				release()
				return nil, err
			}
			return release, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.Join(ctx.Err(), file.Close())
		case <-timer.C:
		}
	}
}
