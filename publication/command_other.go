//go:build !unix

package publication

import (
	"os/exec"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}
