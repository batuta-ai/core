//go:build windows

package loop

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestSupervisionRejectsWindowsNamedPipe(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\batuta-supervision-%d`, os.Getpid())
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	createNamedPipe := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateNamedPipeW")
	const pipeAccessOutbound = 0x2
	handle, _, err := createNamedPipe.Call(uintptr(unsafe.Pointer(name)), pipeAccessOutbound, 0, 1, 4096, 4096, 0, 0)
	if syscall.Handle(handle) == syscall.InvalidHandle {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(syscall.Handle(handle))
	// No writer sends data: a read before handle validation would block.
	if _, err := readSupervisionFile(path, 32); err == nil || !strings.Contains(err.Error(), "not regular") {
		t.Fatalf("named pipe must open and fail descriptor validation: %v", err)
	}
	// The sole server instance is not disconnected/reconnected, so subsequent
	// opens must fail immediately rather than waiting for an available instance.
	if _, err := readSupervisionFile(path, 32); err == nil {
		t.Fatal("accepted busy named pipe")
	}
}

func TestSupervisionRejectsWindowsDevice(t *testing.T) {
	if _, err := readSupervisionFile("NUL", 32); err == nil || !strings.Contains(err.Error(), "not regular") {
		t.Fatalf("character device must fail descriptor validation: %v", err)
	}
}
