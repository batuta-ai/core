//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Windows symlink creation requires privileges; the transaction failure and
// directory-destination tests run on every platform without those privileges.
func TestCouncilArtefactsRejectSymlink(t *testing.T) {
	t.Parallel()
	d := tempDir(t)
	old := []byte("old json\n")
	if e := os.WriteFile(filepath.Join(d, "council.json"), old, 0o644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(d, "referent"), []byte("unrelated\n"), 0o644); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink("referent", filepath.Join(d, "council.md")); e != nil {
		t.Fatal(e)
	}
	if e := writeCouncilArtifacts(d, map[string][]byte{"council.json": []byte("new json"), "council.md": []byte("new report")}); e == nil {
		t.Fatal("publication accepted symlink")
	}
	got, e := os.ReadFile(filepath.Join(d, "council.json"))
	if e != nil || !bytes.Equal(got, old) {
		t.Fatalf("old artifact changed: %q %v", got, e)
	}
	got, e = os.ReadFile(filepath.Join(d, "referent"))
	if e != nil || string(got) != "unrelated\n" {
		t.Fatalf("referent changed: %q %v", got, e)
	}
	info, e := os.Lstat(filepath.Join(d, "council.md"))
	if e != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink lost: %v", e)
	}
	entries, e := os.ReadDir(d)
	if e != nil || len(entries) != 3 {
		t.Fatalf("staged files remain: %v %v", entries, e)
	}
}
