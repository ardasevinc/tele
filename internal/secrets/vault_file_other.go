//go:build !linux

package secrets

import "os"

func openVaultFile(path string) (*os.File, error) {
	// #nosec G304 -- path is an explicitly selected local vault file.
	return os.Open(path)
}
