package loop

import (
	"syscall"
	"unsafe"
)

var panelConsoleInfo = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleScreenBufferInfo")

type panelConsoleBuffer struct {
	Size, Cursor struct{ X, Y int16 }
	Attributes   uint16
	Window       struct{ Left, Top, Right, Bottom int16 }
	Maximum      struct{ X, Y int16 }
}

func readConsoleSize(fd uintptr) (panelConsoleBuffer, bool) {
	var info panelConsoleBuffer
	result, _, _ := panelConsoleInfo.Call(fd, uintptr(unsafe.Pointer(&info)))
	return info, result != 0
}

// TerminalSize reports the visible console window, falling back to 120 by 40.
func TerminalSize(fd uintptr) (width, height int) {
	info, ok := readConsoleSize(fd)
	width = int(info.Window.Right-info.Window.Left) + 1
	height = int(info.Window.Bottom-info.Window.Top) + 1
	if !ok || width <= 0 || height <= 0 {
		return 120, 40
	}
	return width, height
}
func terminalIsTTY(fd uintptr) bool { _, ok := readConsoleSize(fd); return ok }
