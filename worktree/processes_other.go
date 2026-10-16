//go:build !linux && !darwin

package worktree

func platformProcessScanner() ProcessScanner { return unsupportedScanner{} }

func platformParentPID(int) (int, error) { return 0, ErrProcessScanUnsupported }
