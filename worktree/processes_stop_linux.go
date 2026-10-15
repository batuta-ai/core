package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

func platformProcessInspector(pid int) (string, string, string, time.Time, error) {
	proc, state, err := linuxProcessStat(pid)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", "", time.Time{}, os.ErrNotExist
	}
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	if state == 'Z' {
		return proc.Command, "", "Z", proc.Start, nil
	}
	cwd, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", "", time.Time{}, os.ErrNotExist
	}
	return proc.Command, cwd, string(state), proc.Start, err
}

type linuxSignaller struct{}

type linuxSignal struct {
	pid int
	fd  int
}

func platformProcessSignaller() processSignaller { return linuxSignaller{} }

func (linuxSignaller) Open(pid int) (processSignal, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err == nil {
		return linuxSignal{pid: pid, fd: fd}, nil
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return linuxSignal{pid: pid, fd: -1}, nil
	}
	return nil, err
}

func (s linuxSignal) Signal(name string) error {
	sig := unix.SIGTERM
	if name == "KILL" {
		sig = unix.SIGKILL
	}
	if s.fd >= 0 {
		return unix.PidfdSendSignal(s.fd, sig, nil, 0)
	}
	return unix.Kill(s.pid, sig)
}

func (s linuxSignal) Close() error {
	if s.fd >= 0 {
		return unix.Close(s.fd)
	}
	return nil
}
