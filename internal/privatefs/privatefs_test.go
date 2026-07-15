package privatefs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFileReplacesAndRepairsModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "value")
	if err := os.WriteFile(path, []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q", got)
	}
	assertMode(t, dir, DirMode)
	assertMode(t, path, FileMode)
}

func TestAtomicReplacePreservesOriginalAndRemovesTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value")
	if err := os.WriteFile(path, []byte("original"), FileMode); err != nil {
		t.Fatal(err)
	}
	want := errors.New("interrupted")
	err := AtomicReplace(path, func(w io.Writer) error {
		_, _ = w.Write([]byte("partial"))
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("original replaced by %q", got)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".tele-tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestAtomicReplacePreservesOriginalOnShortWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, []byte("original"), FileMode); err != nil {
		t.Fatal(err)
	}
	err := AtomicReplace(path, func(w io.Writer) error {
		_, _ = w.Write([]byte("partial"))
		return io.ErrShortWrite
	})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want short write", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("short write replaced original with %q", got)
	}
}

func TestRepairFileTightensExistingModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "value")
	if err := os.WriteFile(path, []byte("value"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := RepairFile(path); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, DirMode)
	assertMode(t, path, FileMode)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
