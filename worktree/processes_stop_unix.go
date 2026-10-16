//go:build linux || darwin

package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type processInspector func(ctx context.Context, pid int) (command, cwd, state string, start time.Time, err error)

type stopEnv struct {
	home      string
	callerPID int
	parentPID func(pid int) (int, error)
	inspect   processInspector
	signaller processSignaller
}

type unixProcessStopper struct{}

func platformProcessStopper() ProcessStopper { return unixProcessStopper{} }

func (unixProcessStopper) Stop(ctx context.Context, dir string, procs []Process, grace time.Duration) ([]StoppedProcess, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("worktree: find home directory: %w", err)
	}
	return stopWith(ctx, stopEnv{
		home: home, callerPID: os.Getpid(), parentPID: platformParentPID,
		inspect: platformProcessInspector, signaller: platformProcessSignaller(),
	}, dir, procs, grace)
}

func stopWith(ctx context.Context, env stopEnv, dir string, procs []Process, grace time.Duration) ([]StoppedProcess, error) {
	if err := safeProcessTarget(dir, env.home); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
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
	if grace <= 0 {
		grace = 3 * time.Second
	}
	stopped := make([]StoppedProcess, 0, len(procs))
	var failures []error
	for _, proc := range procs {
		if err := ctx.Err(); err != nil {
			return stopped, err
		}
		entry := StoppedProcess{Process: proc}
		switch {
		case proc.PID < 1:
			entry.Reason = "invalid PID"
		case ancestors[proc.PID]:
			entry.Reason = "calling process or ancestor"
		default:
			entry.Signal, entry.Reason, err = stopOne(ctx, env, canonical, proc, grace)
			if err != nil {
				failures = append(failures, fmt.Errorf("PID %d: %w", proc.PID, err))
			}
		}
		stopped = append(stopped, entry)
	}
	return stopped, errors.Join(failures...)
}

const processStillRunning = "process still running"

func stopOne(ctx context.Context, env stopEnv, dir string, proc Process, grace time.Duration) (string, string, error) {
	handle, err := env.signaller.Open(proc.PID)
	if err != nil {
		return "", "could not open process", err
	}
	defer handle.Close()
	alive, reason, err := processAlive(ctx, env.inspect, dir, proc)
	if err != nil || !alive {
		return "", reason, err
	}
	if err := ctx.Err(); err != nil {
		return "", "cancelled before TERM", err
	}
	if err := handle.Signal("TERM"); err != nil {
		return "", "could not send TERM", err
	}
	reason, err = waitProcess(ctx, env.inspect, dir, proc, grace)
	if err != nil {
		return "TERM", "could not confirm exit", err
	}
	if reason != processStillRunning {
		return "TERM", reason, nil
	}
	alive, reason, err = processAlive(ctx, env.inspect, dir, proc)
	if err != nil {
		return "TERM", "could not recheck before KILL", err
	}
	if !alive {
		return "TERM", exitReason(reason), nil
	}
	if err := ctx.Err(); err != nil {
		return "TERM", "cancelled before KILL", err
	}
	if err := handle.Signal("KILL"); err != nil {
		return "TERM", "could not send KILL", err
	}
	reason, err = waitProcess(ctx, env.inspect, dir, proc, 3*time.Second)
	if err != nil {
		return "KILL", "could not confirm exit", err
	}
	if reason == processStillRunning {
		return "KILL", reason, fmt.Errorf("process did not end after KILL")
	}
	return "KILL", reason, nil
}

// waitProcess polls until the process ends or the duration passes. It returns
// "" for a confirmed end, processStillRunning at the deadline, and any other
// reason when the PID stopped matching the process without a confirmed end.
func waitProcess(ctx context.Context, inspect processInspector, dir string, proc Process, duration time.Duration) (string, error) {
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive, reason, err := processAlive(ctx, inspect, dir, proc)
		if err != nil {
			return "", err
		}
		if !alive {
			return exitReason(reason), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return processStillRunning, nil
		case <-ticker.C:
		}
	}
}

// exitReason maps a confirmed end (gone or zombie) to "" and keeps every
// other reason, so an identity or directory change is not reported as a stop.
func exitReason(reason string) string {
	if reason == "process ended" || reason == "process is a zombie" {
		return ""
	}
	return reason
}

func processAlive(ctx context.Context, inspect processInspector, dir string, proc Process) (bool, string, error) {
	command, cwd, state, start, err := inspect(ctx, proc.PID)
	if errors.Is(err, os.ErrNotExist) {
		return false, "process ended", nil
	}
	if err != nil {
		return false, "could not inspect process", err
	}
	if state == "Z" {
		return false, "process is a zombie", nil
	}
	if command != proc.Command || !start.Equal(proc.Start) {
		return false, "process identity changed", nil
	}
	if !filepath.IsAbs(cwd) {
		return false, "working directory unavailable", nil
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return false, "working directory unavailable", nil
	}
	if !pathWithin(dir, resolved) {
		return false, "process left worktree", nil
	}
	original, err := filepath.EvalSymlinks(proc.Cwd)
	if err != nil || !pathWithin(dir, original) || resolved != original {
		return false, "working directory changed", nil
	}
	return true, "", nil
}
