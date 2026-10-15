package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func platformProcessInspector(pid int) (string, string, string, time.Time, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.ESRCH) || info == nil && err == nil {
		return "", "", "", time.Time{}, os.ErrNotExist
	}
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	command := unix.ByteSliceToString(info.Proc.P_comm[:])
	start := time.Unix(info.Proc.P_starttime.Sec, int64(info.Proc.P_starttime.Usec)*1000)
	if info.Proc.P_stat == darwinZombieState {
		return command, "", "Z", start, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, lsofPath, "-n", "-P", "-p", strconv.Itoa(pid), "-a", "-d", "cwd", "-F", "n").Output()
	if ctx.Err() != nil {
		return "", "", "", time.Time{}, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", "", "", time.Time{}, os.ErrNotExist
		}
		return "", "", "", time.Time{}, fmt.Errorf("worktree: inspect cwd of PID %d: %w", pid, err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "n") && len(line) > 1 {
			return command, line[1:], "S", start, nil
		}
	}
	return "", "", "", time.Time{}, fmt.Errorf("worktree: PID %d has no working directory", pid)
}

type darwinSignaller struct{}

type darwinSignal struct{ pid int }

func platformProcessSignaller() processSignaller { return darwinSignaller{} }

func (darwinSignaller) Open(pid int) (processSignal, error) { return darwinSignal{pid: pid}, nil }

// Darwin has no pidfd, so a PID can be reused between the identity recheck and kill.
func (s darwinSignal) Signal(name string) error {
	if name == "KILL" {
		return unix.Kill(s.pid, unix.SIGKILL)
	}
	return unix.Kill(s.pid, unix.SIGTERM)
}

func (darwinSignal) Close() error { return nil }
