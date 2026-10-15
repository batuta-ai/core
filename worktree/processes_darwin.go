package worktree

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

var lsofPath = "/usr/sbin/lsof"

// SZOMB is 5 in Darwin's sys/proc.h; x/sys/unix does not export it.
const darwinZombieState = 5

type darwinProcessScanner struct{}

func platformProcessScanner() ProcessScanner { return darwinProcessScanner{} }

func platformParentPID(pid int) (int, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if info == nil {
		return 0, fmt.Errorf("worktree: PID %d not found", pid)
	}
	return int(info.Eproc.Ppid), nil
}

func (darwinProcessScanner) Inside(ctx context.Context, _ string) ([]Process, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, runErr := exec.CommandContext(ctx, lsofPath, "-n", "-P", "-a", "-d", "cwd", "-F", "pcn").Output()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("worktree: lsof process scan: %w", runErr)
		}
	}
	processes, parseErr := parseLsof(output)
	var found []Process
	for _, proc := range processes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := unix.SysctlKinfoProc("kern.proc.pid", proc.PID)
		if err != nil || info == nil || info.Proc.P_stat == darwinZombieState {
			continue
		}
		proc.PPID = int(info.Eproc.Ppid)
		proc.Start = time.Unix(info.Proc.P_starttime.Sec, int64(info.Proc.P_starttime.Usec)*1000)
		found = append(found, proc)
	}
	if runErr != nil {
		return found, &PartialScanError{Warning: "lsof could not read every process"}
	}
	return found, parseErr
}
