package acp

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestWindowsACPUnavailableBeforeLaunch(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("cmd", "/c", "exit 0")
	process, err := StartProcess(context.Background(), cmd, Options{})
	if !errors.Is(err, ErrProcessUnavailable) || process != nil || cmd.Process != nil {
		t.Fatalf("unqualified platform launched ACP: %v / %v / %v", process, err, cmd.Process)
	}
}
