//go:build unix

package loop

import (
	"syscall"
	"unsafe"
)

type terminalWindowSize struct{ rows, columns, xpixel, ypixel uint16 }

func readTerminalSize(fd uintptr) (terminalWindowSize, bool) {
	var size terminalWindowSize
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	return size, err == 0
}

// TerminalSize reports columns and rows, falling back to 120 by 40 off-terminal.
func TerminalSize(fd uintptr) (width, height int) {
	size, ok := readTerminalSize(fd)
	if !ok || size.columns == 0 || size.rows == 0 {
		return 120, 40
	}
	return int(size.columns), int(size.rows)
}
func terminalIsTTY(fd uintptr) bool { _, ok := readTerminalSize(fd); return ok }
