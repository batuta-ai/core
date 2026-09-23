package executor

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/core/executor/acp"
)

// WorktreePermissionPolicy allows a request only when every named location
// resolves inside Request.Cwd. It never selects allow_always; requests with
// no locations, including execute, are rejected.
func WorktreePermissionPolicy(_ context.Context, execution Execution, request acp.PermissionRequest) string {
	if len(request.ToolCall.Locations) == 0 || !filepath.IsAbs(execution.Request.Cwd) {
		return ""
	}
	cwd, err := filepath.EvalSymlinks(execution.Request.Cwd)
	if err != nil {
		return ""
	}
	for _, location := range request.ToolCall.Locations {
		resolved, ok := resolveWorktreeLocation(location.Path)
		if !ok || !insideWorktree(cwd, resolved) {
			return ""
		}
	}
	for _, option := range request.Options {
		if option.Kind == "allow_once" {
			return option.OptionID
		}
	}
	return ""
}

func resolveWorktreeLocation(path string) (string, bool) {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return "", false
	}
	var rest []string
	current := cleaned
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...), true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		rest = append([]string{filepath.Base(current)}, rest...)
		current = parent
	}
}

func insideWorktree(cwd, path string) bool {
	return path == cwd || strings.HasPrefix(path, cwd+string(filepath.Separator))
}
