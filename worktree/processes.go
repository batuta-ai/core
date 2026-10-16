package worktree

import (
	"context"
	"os"
)

func ProcessesInside(ctx context.Context, dir string) ([]Process, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return insideWith(ctx, insideEnv{
		scanner:   platformProcessScanner(),
		home:      home,
		callerPID: os.Getpid(),
		parentPID: platformParentPID,
	}, dir)
}
