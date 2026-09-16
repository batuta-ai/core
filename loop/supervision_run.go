package loop

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// Bind configuration to the runner's durable identity, never the mutable plan
// path (which finalization archives). Review jobs retain their existing identity.
func runnerSupervision(root, delivery string, config SuperviseOptions) (SuperviseOptions, error) {
	if (config.Observer.Workspace != "" && filepath.Clean(config.Observer.Workspace) != root) || (config.Observer.Delivery != "" && config.Observer.Delivery != delivery) {
		return config, errors.New("loop: supervisor belongs to another delivery")
	}
	config.Observer.Workspace, config.Observer.Delivery = root, delivery
	observer, err := normalizeSupervisionOptions(config.Observer)
	if err != nil {
		return config, err
	}
	config.Observer = observer
	if config.Interval == 0 {
		config.Interval = time.Second
	}
	if config.Interval < 100*time.Millisecond || config.Interval > time.Minute {
		return config, errors.New("loop: supervision interval must be between 100ms and 1m")
	}
	if config.Policy != nil && config.Policy.Delivery != delivery {
		return config, errors.New("loop: supervision policy belongs to another delivery")
	}
	return config, nil
}

func (r *Runner) runSupervised(ctx context.Context, config SuperviseOptions) (state string, runErr error) {
	// Resume already holds ownership, including when configuration is invalid.
	defer func() { runErr = errors.Join(runErr, r.Release()) }()
	config, err := runnerSupervision(r.root, r.delivery, config)
	if err != nil {
		return "", err
	}
	// Both paths can share a caller's unguarded writer, such as bytes.Buffer.
	if config.Output == nil || config.Output == r.opts.Stdout {
		config.Output = r.out
	}
	passive := config
	passive.Review, passive.Policy, passive.Execution = nil, nil, nil
	passive.Once = false
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	observeCtx, stopObserver := context.WithCancel(runCtx)
	defer stopObserver()
	var joined chan error
	var observerErr error
	join := func() {
		stopObserver()
		if joined != nil {
			observerErr = <-joined
			joined = nil
		}
	}
	defer join()
	state, runErr = r.runOwned(runCtx, func() error {
		initial := passive
		initial.Once = true
		if err := Supervise(observeCtx, initial); err != nil {
			return err
		}
		joined = make(chan error, 1)
		go func() {
			err := Supervise(observeCtx, passive)
			if err != nil {
				cancelRun()
			}
			joined <- err
		}()
		return nil
	})
	join()
	runErr = errors.Join(runErr, observerErr, ctx.Err())
	if runErr != nil {
		return state, runErr
	}
	// Ownership and passive activity are now joined. One bounded pass reuses the
	// existing review budget and continuation journal; it adds no retry owner.
	config.Once = true
	if config.Execution != nil {
		execution := *config.Execution
		// The continuation and its observer must share this lock when they
		// inherit the foreground writer; independent wrappers do not serialize it.
		if execution.Stdout == r.opts.Stdout {
			execution.Stdout = r.out
		}
		child := passive
		child.Observer = config.Observer
		execution.Supervisor = &child
		config.Execution = &execution
	}
	if err := Supervise(ctx, config); err != nil {
		return state, err
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	observation, err := ObserveSupervision(config.Observer)
	if err != nil {
		return state, err
	}
	if observation.TerminalState != "" {
		state = observation.TerminalState
	}
	if state != StateDone {
		return state, nil
	}
	gate, err := CheckSupervisionGate(ctx, config.Observer)
	if err != nil || !gate.Cleared {
		return StateReviewBlocked, err
	}
	return state, nil
}
