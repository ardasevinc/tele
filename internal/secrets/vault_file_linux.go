//go:build linux

package secrets

import (
	"os"

	"golang.org/x/sys/unix"
)

func openVaultFile(path string) (*os.File, error) {
	// #nosec G304 -- path is an explicitly selected local vault file.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
