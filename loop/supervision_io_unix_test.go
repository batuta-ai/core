//go:build unix

package loop

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSupervisionRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSupervisionFile(path, 32); err == nil {
		t.Fatal("accepted FIFO without a writer")
	}
}

func TestSupervisionRejectsSymlinkToFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "link")
	if err := os.Symlink(fifo, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readSupervisionFile(path, 32); err == nil {
		t.Fatal("accepted symlink to FIFO")
	}
}

func TestSupervisionReplacementValidatesOpenedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte("regular"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("initial pathname: %v, %v", info, err)
	}
	// Replace a checked regular pathname before opening it.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openSupervisionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Replace it again before validation: pathname metadata now says regular,
	// but the opened descriptor still refers to the FIFO.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSupervisionOpenedFile(file, 32); err == nil {
		t.Fatal("validated replacement pathname instead of opened FIFO")
	}
	data, err := readSupervisionFile(path, 32)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement regular file: %q, %v", data, err)
	}
}

func TestSupervisionPersistencePrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	for range 2 {
		if err := writeSupervisionJSON(path, "private"); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("persistence broadened permissions: %v", info.Mode())
		}
	}
}
