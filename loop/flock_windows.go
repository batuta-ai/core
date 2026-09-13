//go:build windows

package loop

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	presenceKernel32     = syscall.NewLazyDLL("kernel32.dll")
	presenceLockFileEx   = presenceKernel32.NewProc("LockFileEx")
	presenceUnlockFileEx = presenceKernel32.NewProc("UnlockFileEx")
)

func lockExclusive(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, err := presenceLockFileEx.Call(file.Fd(), 2, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return err
	}
	return nil
}

func tryLockExclusive(file *os.File) (bool, error) {
	const (
		lockfileExclusiveLock   = 2
		lockfileFailImmediately = 1
		errorLockViolation      = syscall.Errno(33)
	)
	var overlapped syscall.Overlapped
	result, _, err := presenceLockFileEx.Call(file.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result != 0 {
		return true, nil
	}
	if errors.Is(err, errorLockViolation) {
		return false, nil
	}
	return false, err
}

func unlockFile(file *os.File) {
	var overlapped syscall.Overlapped
	presenceUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
}
