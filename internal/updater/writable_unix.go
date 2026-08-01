//go:build darwin || linux

package updater

import (
	"os"

	"golang.org/x/sys/unix"
)

func directoryWritable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir() && unix.Access(path, unix.W_OK) == nil
}
