//go:build !darwin && !linux

package updater

import "os"

func directoryWritable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir() && info.Mode().Perm()&0o222 != 0
}
