//go:build windows

package loop

import (
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

func unlockFile(file *os.File) {
	var overlapped syscall.Overlapped
	presenceUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
}
