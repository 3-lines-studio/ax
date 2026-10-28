//go:build !unix

package routine

import "os"

func lockRun(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

func unlockRun(file *os.File) { _ = file.Close() }
