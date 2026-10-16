//go:build !linux && !darwin

package worktree

func platformProcessStopper() ProcessStopper { return unsupportedStopper{} }
