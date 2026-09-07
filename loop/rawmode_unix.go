//go:build unix

package loop

import (
	"context"
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

type panelFileTerminal struct {
	file     *os.File
	sequence string
}

func newPanelTerminal(file *os.File) panelTerminal {
	if !terminalIsTTY(file.Fd()) {
		return nil
	}
	return &panelFileTerminal{file: file}
}

func panelTermios(fd uintptr, set bool, state *syscall.Termios) error {
	// Linux uses TCGETS/TCSETS; Darwin and the BSDs use TIOCGETA/TIOCSETA.
	request := uintptr(0x5401)
	if runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" || runtime.GOOS == "netbsd" || runtime.GOOS == "dragonfly" {
		request = 0x40487413
		if set {
			request = 0x80487414
		}
	} else if set {
		request = 0x5402
	}
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(unsafe.Pointer(state)))
	if err != 0 {
		return err
	}
	return nil
}

func (t *panelFileTerminal) enterRaw() (func() error, error) {
	var original syscall.Termios
	if err := panelTermios(t.file.Fd(), false, &original); err != nil {
		return nil, err
	}
	raw := original
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	// Keep output processing so the renderer's newlines still return to column one.
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 1
	if err := panelTermios(t.file.Fd(), true, &raw); err != nil {
		return nil, err
	}
	t.sequence = ""
	return func() error { return panelTermios(t.file.Fd(), true, &original) }, nil
}

func (t *panelFileTerminal) readKey(ctx context.Context) (string, error) {
	var buffer [1]byte
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := syscall.Read(int(t.file.Fd()), buffer[:])
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return "", err
		}
		if n == 0 {
			t.sequence = ""
			continue
		}
		if key := decodePanelKey(&t.sequence, buffer[0]); key != "" {
			return key, nil
		}
	}
}
