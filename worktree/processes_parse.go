package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Process struct {
	PID, PPID    int
	Command, Cwd string
	Start        time.Time
}

var (
	ErrProcessScanUnsupported = errors.New("worktree: process scan is unsupported on this platform")
	ErrUnsafeProcessTarget    = errors.New("worktree: unsafe process scan target")
)

type PartialScanError struct {
	Warning string
}

func (e *PartialScanError) Error() string {
	return "worktree: partial process scan: " + e.Warning
}

type ProcessScanner interface {
	Inside(ctx context.Context, dir string) ([]Process, error)
}

type unsupportedScanner struct{}

func (unsupportedScanner) Inside(context.Context, string) ([]Process, error) {
	return nil, ErrProcessScanUnsupported
}

type insideEnv struct {
	scanner   ProcessScanner
	home      string
	callerPID int
	parentPID func(pid int) (int, error)
}

func insideWith(ctx context.Context, env insideEnv, dir string) ([]Process, error) {
	if err := safeProcessTarget(dir, env.home); err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("worktree: inspect process target: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("worktree: resolve process target: %w", err)
	}
	canonical = filepath.Clean(canonical)
	if err := safeProcessTarget(canonical, env.home); err != nil {
		return nil, err
	}
	ancestors, err := processAncestors(env.callerPID, env.parentPID)
	if err != nil {
		return nil, err
	}
	procs, scanErr := env.scanner.Inside(ctx, canonical)
	var partial *PartialScanError
	if scanErr != nil && !errors.As(scanErr, &partial) {
		return nil, scanErr
	}
	inside := make([]Process, 0, len(procs))
	for _, proc := range procs {
		if ancestors[proc.PID] || !filepath.IsAbs(proc.Cwd) {
			continue
		}
		cwd, err := filepath.EvalSymlinks(proc.Cwd)
		if err != nil {
			continue
		}
		if pathWithin(canonical, cwd) {
			inside = append(inside, proc)
		}
	}
	return inside, scanErr
}

func safeProcessTarget(dir, home string) error {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || pathElements(dir) < 3 {
		return ErrUnsafeProcessTarget
	}
	if home != "" {
		if pathWithin(dir, home) {
			return ErrUnsafeProcessTarget
		}
		if resolved, err := filepath.EvalSymlinks(home); err == nil && pathWithin(dir, resolved) {
			return ErrUnsafeProcessTarget
		}
	}
	return nil
}

func pathElements(path string) int {
	relative := strings.TrimPrefix(path, filepath.VolumeName(path))
	relative = strings.Trim(relative, string(filepath.Separator))
	if relative == "" {
		return 0
	}
	return len(strings.Split(relative, string(filepath.Separator)))
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func processAncestors(callerPID int, parentPID func(int) (int, error)) (map[int]bool, error) {
	if callerPID < 1 || parentPID == nil {
		return nil, errors.New("worktree: cannot identify calling process ancestry")
	}
	ancestors := make(map[int]bool)
	for pid := callerPID; ; {
		if ancestors[pid] {
			return ancestors, nil
		}
		ancestors[pid] = true
		if pid == 1 {
			return ancestors, nil
		}
		parent, err := parentPID(pid)
		if err != nil {
			return nil, fmt.Errorf("worktree: inspect parent of PID %d: %w", pid, err)
		}
		if parent < 1 {
			return nil, fmt.Errorf("worktree: invalid parent of PID %d: %d", pid, parent)
		}
		pid = parent
	}
}

func parseLsof(data []byte) ([]Process, error) {
	var processes []Process
	var current Process
	var havePID, haveCommand, haveCwd, partial bool
	finish := func() {
		if !havePID {
			return
		}
		if haveCommand && haveCwd {
			processes = append(processes, current)
		} else {
			partial = true
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			finish()
			current = Process{}
			havePID, haveCommand, haveCwd = false, false, false
			pid, err := strconv.Atoi(line[1:])
			if err != nil || pid < 1 {
				partial = true
				continue
			}
			current.PID, havePID = pid, true
		case 'c':
			if havePID {
				current.Command, haveCommand = line[1:], true
			} else {
				partial = true
			}
		case 'n':
			if havePID {
				current.Cwd, haveCwd = line[1:], true
			} else {
				partial = true
			}
		}
	}
	finish()
	if partial {
		return processes, &PartialScanError{Warning: "incomplete lsof process records"}
	}
	return processes, nil
}

func parseProcStat(line string) (Process, byte, error) {
	open := strings.IndexByte(line, '(')
	close := strings.LastIndex(line, ") ")
	if open < 1 || close <= open {
		return Process{}, 0, errors.New("worktree: malformed proc stat command")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line[:open]))
	if err != nil || pid < 1 {
		return Process{}, 0, errors.New("worktree: malformed proc stat PID")
	}
	fields := strings.Fields(line[close+2:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return Process{}, 0, errors.New("worktree: incomplete proc stat fields")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid < 0 {
		return Process{}, 0, errors.New("worktree: malformed proc stat parent PID")
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil || ticks < 0 {
		return Process{}, 0, errors.New("worktree: malformed proc stat start time")
	}
	// The tick count is an identity token; encoding it in Start preserves exact comparison.
	return Process{PID: pid, PPID: ppid, Command: line[open+1 : close], Start: time.Unix(0, ticks)}, fields[0][0], nil
}
