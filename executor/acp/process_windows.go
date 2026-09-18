package acp

import "os/exec"

// Windows needs native runtime evidence for Job Object containment and teardown.
// Until qualified, reject before launch; callers may select CLI before submission.
func processAvailable() error  { return ErrProcessUnavailable }
func prepareProcess(*exec.Cmd) {}

type ownedProcesses struct{}

func (*ownedProcesses) track(int, <-chan struct{}) {}

func (p *Process) shutdownGroup() error {
	p.Connection.Close()
	return ErrProcessUnavailable
}
