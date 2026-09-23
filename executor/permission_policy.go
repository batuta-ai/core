package executor

import (
	"context"
	"errors"
	"os"
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

// worktreeLocationHasDotDot reports whether the path has a ".." element
// between separators the platform accepts: '/' is one on every platform Go
// supports, '\\' only on Windows, so splitting on filepath.Separator alone
// would let a slash-written ".." reach filepath.Clean.
func worktreeLocationHasDotDot(path string) bool {
	for start := 0; start <= len(path); {
		end := start
		for end < len(path) && !os.IsPathSeparator(path[end]) {
			end++
		}
		if path[start:end] == ".." {
			return true
		}
		start = end + 1
	}
	return false
}

func resolveWorktreeLocation(path string) (string, bool) {
	if worktreeLocationHasDotDot(path) {
		return "", false
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return "", false
	}
	var rest []string
	current := cleaned
	for {
		_, err := os.Lstat(current)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", false
			}
			parent := filepath.Dir(current)
			if parent == current {
				return "", false
			}
			rest = append([]string{filepath.Base(current)}, rest...)
			current = parent
			continue
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return "", false
		}
		return filepath.Join(append([]string{resolved}, rest...)...), true
	}
}

func insideWorktree(cwd, path string) bool {
	return path == cwd || strings.HasPrefix(path, cwd+string(filepath.Separator))
}
