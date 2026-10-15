//go:build darwin || linux

package journal

import (
	"errors"
	"os"
	"syscall"
)

func tryLockJournal(file *os.File) (func() error, bool, error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() error {
		return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	}, false, nil
}
