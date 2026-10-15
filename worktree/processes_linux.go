package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type linuxProcessScanner struct{}

func platformProcessScanner() ProcessScanner { return linuxProcessScanner{} }

func platformParentPID(pid int) (int, error) {
	proc, _, err := linuxProcessStat(pid)
	return proc.PPID, err
}

func linuxProcessStat(pid int) (Process, byte, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return Process{}, 0, err
	}
	return parseProcStat(string(data))
}

func (linuxProcessScanner) Inside(ctx context.Context, _ string) ([]Process, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("worktree: list processes: %w", err)
	}
	var processes []Process
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid < 1 {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd"))
		if err != nil {
			continue
		}
		proc, state, err := linuxProcessStat(pid)
		if err != nil || state == 'Z' {
			continue
		}
		proc.Cwd = cwd
		processes = append(processes, proc)
	}
	return processes, nil
}
