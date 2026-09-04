package inventory

import (
	"path/filepath"
	"testing"
)

// tempDir returns a canonical temporary directory. On macOS t.TempDir()
// lives under a symlinked /var, and the trusted-root checks compare the
// path against filepath.EvalSymlinks.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return dir
}
