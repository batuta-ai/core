//go:build windows

package loop

import (
	"os"
	"syscall"
	"unsafe"
)

var supervisionMoveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func openSupervisionFile(path string) (*os.File, error) {
	// CreateFile (used by os.Open) fails for busy named pipes without waiting.
	// The caller validates the opened handle before attempting any reads.
	return os.Open(path)
}

func replaceSupervisionFile(source, destination string) error {
	from, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	// The temporary file has already been flushed and closed. Request a
	// write-through replacement; Windows cannot flush a read-only directory.
	const moveFileReplaceExisting = 0x1
	const moveFileWriteThrough = 0x8
	result, _, err := supervisionMoveFileEx.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), moveFileReplaceExisting|moveFileWriteThrough)
	if result == 0 {
		return &os.LinkError{Op: "MoveFileEx", Old: source, New: destination, Err: err}
	}
	return nil
}
