//go:build windows

package journal

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	publicationKernel32     = syscall.NewLazyDLL("kernel32.dll")
	publicationLockFileEx   = publicationKernel32.NewProc("LockFileEx")
	publicationUnlockFileEx = publicationKernel32.NewProc("UnlockFileEx")
)

func lockPublication(file *os.File) (func() error, error) {
	const (
		lockfileExclusiveLock   = 2
		lockfileFailImmediately = 1
		sentinelOffset          = (256 << 20) + 1
	)
	overlapped := syscall.Overlapped{Offset: sentinelOffset}
	result, _, err := publicationLockFileEx.Call(file.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return nil, err
	}
	return func() error {
		result, _, err := publicationUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
		if result == 0 {
			return err
		}
		return nil
	}, nil
}
