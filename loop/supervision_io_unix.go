//go:build unix

package loop

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func openSupervisionFile(path string) (*os.File, error) {
	// A pathname check cannot prevent replacement with a FIFO before open.
	// Nonblocking open lets the caller validate the actual descriptor first.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func replaceSupervisionFile(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
