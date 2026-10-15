//go:build windows

package journal

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	journalKernel32     = syscall.NewLazyDLL("kernel32.dll")
	journalLockFileEx   = journalKernel32.NewProc("LockFileEx")
	journalUnlockFileEx = journalKernel32.NewProc("UnlockFileEx")
)

func tryLockJournal(file *os.File) (func() error, bool, error) {
	const (
		exclusiveLock   = 2
		failImmediately = 1
		sentinelOffset  = (256 << 20) + 1
		lockViolation   = syscall.Errno(33)
	)
	overlapped := syscall.Overlapped{Offset: sentinelOffset}
	result, _, err := journalLockFileEx.Call(file.Fd(), exclusiveLock|failImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result != 0 {
		return func() error {
			result, _, err := journalUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
			if result == 0 {
				return err
			}
			return nil
		}, false, nil
	}
	if errors.Is(err, lockViolation) {
		return nil, true, nil
	}
	return nil, false, err
}
