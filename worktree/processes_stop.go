package worktree

import (
	"context"
	"time"
)

type StoppedProcess struct {
	Process
	Signal, Reason string
}

// Confirmed reports whether a signal ended the process and its end was
// observed. An entry with a signal and a reason was signalled, but its end
// could not be confirmed.
func (s StoppedProcess) Confirmed() bool {
	return (s.Signal == "TERM" || s.Signal == "KILL") && s.Reason == ""
}

type ProcessStopper interface {
	Stop(ctx context.Context, dir string, procs []Process, grace time.Duration) ([]StoppedProcess, error)
}

type processSignaller interface {
	Open(pid int) (processSignal, error)
}

type processSignal interface {
	Signal(name string) error
	Close() error
}

type unsupportedStopper struct{ signaller processSignaller }

func (unsupportedStopper) Stop(context.Context, string, []Process, time.Duration) ([]StoppedProcess, error) {
	return nil, ErrProcessScanUnsupported
}

func StopProcesses(ctx context.Context, dir string, procs []Process, grace time.Duration) ([]StoppedProcess, error) {
	return platformProcessStopper().Stop(ctx, dir, procs, grace)
}
