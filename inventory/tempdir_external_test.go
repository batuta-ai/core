package inventory_test

import (
	"path/filepath"
	"testing"
)

// tempDir mirrors the in-package helper for external tests: a canonical
// temporary directory, resolved through filepath.EvalSymlinks.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return dir
}
