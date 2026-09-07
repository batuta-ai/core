package loop

import (
	"context"
	"os"
	"syscall"
	"unsafe"
)

var panelKernel32 = syscall.NewLazyDLL("kernel32.dll")
var panelGetConsoleMode = panelKernel32.NewProc("GetConsoleMode")
var panelSetConsoleMode = panelKernel32.NewProc("SetConsoleMode")
var panelReadConsoleInput = panelKernel32.NewProc("ReadConsoleInputW")
var panelWaitForInput = panelKernel32.NewProc("WaitForSingleObject")

type panelFileTerminal struct{ file *os.File }

func newPanelTerminal(file *os.File) panelTerminal {
	var mode uint32
	ok, _, _ := panelGetConsoleMode.Call(file.Fd(), uintptr(unsafe.Pointer(&mode)))
	if ok == 0 {
		return nil
	}
	return &panelFileTerminal{file: file}
}

func (t *panelFileTerminal) enterRaw() (func() error, error) {
	var original uint32
	ok, _, err := panelGetConsoleMode.Call(t.file.Fd(), uintptr(unsafe.Pointer(&original)))
	if ok == 0 {
		return nil, err
	}
	// Native key events avoid depending on the console's VT input setting.
	raw := (original &^ uint32(0x1|0x2|0x4|0x40|0x200)) | 0x80
	ok, _, err = panelSetConsoleMode.Call(t.file.Fd(), uintptr(raw))
	if ok == 0 {
		return nil, err
	}
	return func() error {
		ok, _, err := panelSetConsoleMode.Call(t.file.Fd(), uintptr(original))
		if ok == 0 {
			return err
		}
		return nil
	}, nil
}

func (t *panelFileTerminal) readKey(ctx context.Context) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		status, _, err := panelWaitForInput.Call(t.file.Fd(), 100)
		if status == 258 {
			continue
		}
		if status != 0 {
			return "", err
		}
		var event struct {
			kind, padding               uint16
			down                        int32
			repeat, virtual, scan, char uint16
			control                     uint32
		}
		var count uint32
		ok, _, err := panelReadConsoleInput.Call(t.file.Fd(), uintptr(unsafe.Pointer(&event)), 1, uintptr(unsafe.Pointer(&count)))
		if ok == 0 {
			return "", err
		}
		if count == 0 || event.kind != 1 || event.down == 0 {
			continue
		}
		switch event.virtual {
		case 0x26:
			return "up", nil
		case 0x28:
			return "down", nil
		case 0x21:
			return "pageUp", nil
		case 0x22:
			return "pageDown", nil
		}
		if event.char == 3 {
			return "interrupt", nil
		}
		if event.char != 0 {
			return string(rune(event.char)), nil
		}
	}
}
