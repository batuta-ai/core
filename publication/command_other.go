//go:build !unix

package publication

import (
	"os/exec"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}

// Platforms without Unix signals retain direct-child cancellation. ReviewRunner
// still reports unresolved descendant cleanup; no containment is claimed.
func configureReviewProcess(cmd *exec.Cmd) {
	configureProcess(cmd)
}
