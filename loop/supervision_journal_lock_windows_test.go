//go:build windows

package loop

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	supervisionJournalKernel32     = syscall.NewLazyDLL("kernel32.dll")
	supervisionJournalLockFileEx   = supervisionJournalKernel32.NewProc("LockFileEx")
	supervisionJournalUnlockFileEx = supervisionJournalKernel32.NewProc("UnlockFileEx")
)

func lockSupervisionJournalForTest(file *os.File) (func() error, error) {
	const sentinelOffset = (256 << 20) + 1
	overlapped := syscall.Overlapped{Offset: sentinelOffset}
	result, _, err := supervisionJournalLockFileEx.Call(file.Fd(), 2|1, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return nil, err
	}
	return func() error {
		result, _, err := supervisionJournalUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
		if result == 0 {
			return err
		}
		return nil
	}, nil
}
