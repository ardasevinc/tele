package privatefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	DirMode  fs.FileMode = 0o700
	FileMode fs.FileMode = 0o600
)

func EnsureDir(path string) error {
	if err := os.MkdirAll(path, DirMode); err != nil {
		return err
	}
	return os.Chmod(path, DirMode)
}

func RepairFile(path string) error {
	if err := os.Chmod(filepath.Dir(path), DirMode); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Chmod(path, FileMode); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func AtomicWriteFile(path string, data []byte) error {
	return AtomicReplace(path, func(w io.Writer) error {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n != len(data) {
			return io.ErrShortWrite
		}
		return nil
	})
}

func AtomicReplace(path string, write func(io.Writer) error) (err error) {
	return AtomicReplaceFile(path, func(file *os.File) error { return write(file) })
}

func AtomicReplaceFile(path string, write func(*os.File) error) (err error) {
	return atomicReplaceFile(path, write, atomicReplaceOps{
		createTemp: os.CreateTemp,
		syncFile:   (*os.File).Sync,
		closeFile:  (*os.File).Close,
		rename:     os.Rename,
		syncDir:    syncDir,
	})
}

type atomicReplaceOps struct {
	createTemp func(string, string) (*os.File, error)
	syncFile   func(*os.File) error
	closeFile  func(*os.File) error
	rename     func(string, string) error
	syncDir    func(string) error
}

func atomicReplaceFile(path string, write func(*os.File) error, ops atomicReplaceOps) (err error) {
	dir := filepath.Dir(path)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	temp, err := ops.createTemp(dir, ".tele-tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(FileMode); err != nil {
		return err
	}
	if err := write(temp); err != nil {
		return err
	}
	if err := ops.syncFile(temp); err != nil {
		return err
	}
	if err := ops.closeFile(temp); err != nil {
		return err
	}
	temp = nil
	if err := ops.rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	if err := ops.syncDir(dir); err != nil {
		return err
	}
	return nil
}

func syncDir(path string) error {
	// #nosec G304 -- caller intentionally supplies the local state directory to sync.
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
