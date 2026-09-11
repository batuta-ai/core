package acp

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestProcessCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.Command("must-not-launch")
	process, err := StartProcess(ctx, cmd, Options{})
	if !errors.Is(err, context.Canceled) || process != nil || cmd.Process != nil {
		t.Fatalf("cancelled startup: %v / %v", process, err)
	}
}
