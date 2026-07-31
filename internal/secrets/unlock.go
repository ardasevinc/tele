package secrets

import (
	"fmt"
	"io"
	"os"
)

const MaxPassphraseSize = 64 << 10

func ReadPassphraseFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !vaultOwnerAllowed(info) {
		return nil, fmt.Errorf("vault passphrase file must be a protected regular file owned by the current user or root")
	}
	file, err := openVaultFile(path)
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o077 != 0 || !vaultOwnerAllowed(openedInfo) || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("vault passphrase file changed during secure open")
	}
	return readPassphrase(file)
}

func ReadPassphraseFD(fd int) ([]byte, error) {
	if fd < 3 {
		return nil, fmt.Errorf("vault passphrase fd must be at least 3")
	}
	file := os.NewFile(uintptr(fd), fmt.Sprintf("vault-passphrase-fd-%d", fd))
	if file == nil {
		return nil, fmt.Errorf("invalid vault passphrase fd %d", fd)
	}
	return readPassphrase(file)
}

func readPassphrase(reader io.ReadCloser) ([]byte, error) {
	defer reader.Close()
	value, err := io.ReadAll(io.LimitReader(reader, MaxPassphraseSize+1))
	if err != nil {
		zeroBytes(value)
		return nil, err
	}
	if len(value) > MaxPassphraseSize {
		zeroBytes(value)
		return nil, fmt.Errorf("vault passphrase exceeds %d bytes", MaxPassphraseSize)
	}
	value = trimOneLineEnding(value)
	if len(value) == 0 {
		return nil, fmt.Errorf("vault passphrase must not be empty")
	}
	return value, nil
}

func trimOneLineEnding(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
		if len(value) > 0 && value[len(value)-1] == '\r' {
			value = value[:len(value)-1]
		}
	}
	return value
}
