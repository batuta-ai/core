//go:build !unix

package routing

import "os"

// Without flock the ownership journal relies on the in-process mutex alone.
// The daemon that writes this journal runs on unix hosts; on other
// platforms the `batuta` binary only needs the package to compile — the
// loop never opens the ownership store.
func lockExclusive(*os.File) error { return nil }

func unlockFile(*os.File) {}
