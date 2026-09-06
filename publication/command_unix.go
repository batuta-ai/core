//go:build unix

package publication

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcess puts the command in its own process group and, when the
// context ends, kills the whole group: executor CLIs spawn children (node,
// shells, test runners) that would otherwise outlive the timeout and keep
// the output pipes open. WaitDelay bounds the wait for those pipes.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}
