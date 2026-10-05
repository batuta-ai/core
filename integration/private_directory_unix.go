//go:build !windows

package integration

import (
	"errors"
	"os"
)

func makePrivateDirectory(path string) error { return os.Mkdir(path, 0700) }

func checkDirectoryPrivacy(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("integration: scratch directory is not private")
	}
	return nil
}
