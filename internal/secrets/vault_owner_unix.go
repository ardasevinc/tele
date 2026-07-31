//go:build !windows

package secrets

import (
	"io/fs"
	"os"
	"syscall"
)

func vaultOwnerAllowed(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return stat.Uid == 0 || stat.Uid == uint32(os.Geteuid())
}
