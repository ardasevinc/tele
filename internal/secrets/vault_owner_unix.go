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
	effectiveUID := os.Geteuid()
	return stat.Uid == 0 || (effectiveUID >= 0 && uint64(stat.Uid) == uint64(effectiveUID))
}
