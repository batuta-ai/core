//go:build unix

package routing

import (
	"os"
	"syscall"
)

// lockExclusive takes an advisory exclusive lock on the ownership journal's
// lock file; unlockFile releases it.
func lockExclusive(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func unlockFile(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
