package acp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"
)

var (
	ErrProcessUnavailable = errors.New("acp: owned process execution unavailable on this platform")
	ErrCleanupUnresolved  = errors.New("acp: owned descendant cleanup unresolved")
	ErrProcessStart       = errors.New("acp: worker process did not start")
)

// Process owns a single worker attempt and its transport. Shutdown proves direct
// child exit or returns an error; descendant uncertainty always remains an error.
// A protocol result, including cancelled, is never evidence of process exit.
type Process struct {
	Connection  *Connection
	cmd         *exec.Cmd
	exited      chan struct{}
	stop        chan struct{}
	tracked     chan struct{}
	owned       ownedProcesses
	once        sync.Once
	shutdownErr error
}

// StartProcess consumes an unstarted exec.Command with no stdio or SysProcAttr.
// Environment, working directory and arguments remain the caller's responsibility.
// The caller must call Shutdown on every outcome, including context cancellation;
// the context only guards startup so session/cancel can precede process shutdown.
// Stderr is discarded instead of retaining untrusted diagnostics or credentials.
func StartProcess(ctx context.Context, cmd *exec.Cmd, options Options) (*Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := processAvailable(); err != nil {
		return nil, err
	}
	if cmd == nil || cmd.Process != nil || cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil || cmd.SysProcAttr != nil {
		return nil, ErrConfiguration
	}
	// A file avoids exec.Cmd's copy goroutine: descendants can inherit stderr
	// without keeping Wait blocked after the direct child exits.
	stderr, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, ErrProcessStart
	}
	defer stderr.Close()
	input, writer, err := os.Pipe()
	if err != nil {
		return nil, ErrProcessStart
	}
	defer input.Close()
	reader, output, err := os.Pipe()
	if err != nil {
		writer.Close()
		return nil, ErrProcessStart
	}
	defer output.Close()
	conn, err := NewConnection(reader, writer, options)
	if err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, stderr
	prepareProcess(cmd)
	if err := cmd.Start(); err != nil {
		conn.Close()
		return nil, ErrProcessStart
	}
	p := &Process{Connection: conn, cmd: cmd, exited: make(chan struct{}), stop: make(chan struct{}), tracked: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	go func() {
		defer close(p.tracked)
		p.owned.track(cmd.Process.Pid, p.stop)
	}()
	return p, nil
}

// Shutdown is concurrent-safe and bounded. Unix discovery cannot exclude a fork
// and reparent between snapshots, or atomically bind a discovered PID to a signal.
// Consequently it never signals descendants or reports verified tree cleanup.
// It kills only the retained os.Process (whose Signal/Wait synchronize PID reuse).
// A future platform containment implementation can strengthen this contract.
func (p *Process) Shutdown() error {
	p.once.Do(func() {
		p.Connection.Close()
		close(p.stop)
		grace := time.NewTimer(100 * time.Millisecond)
		defer grace.Stop()
		select {
		case <-p.exited:
		case <-grace.C:
			_ = p.cmd.Process.Kill()
		}
		limit := time.NewTimer(time.Second)
		defer limit.Stop()
		select {
		case <-p.exited:
		case <-limit.C:
		}
		// Discovery commands have their own short deadline and bounded output.
		<-p.tracked
		p.shutdownErr = ErrCleanupUnresolved
	})
	return p.shutdownErr
}
