//go:build windows

package secrets

import "io/fs"

func vaultOwnerAllowed(fs.FileInfo) bool {
	return true
}
