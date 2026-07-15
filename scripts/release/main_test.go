package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteArchiveIsCanonicalAndDeterministic(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "input")
	if err := os.WriteFile(binary, []byte("tele-test"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(dir, "first.tar.gz"), filepath.Join(dir, "second.tar.gz")
	if err := writeArchive(first, binary); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(binary, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := writeArchive(second, binary); err != nil {
		t.Fatal(err)
	}
	one, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Fatal("archive bytes changed with source metadata")
	}
	zipper, err := gzip.NewReader(bytes.NewReader(one))
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(zipper)
	header, err := archive.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "tele" || header.Mode != 0o755 || !header.ModTime.Equal(time.Unix(0, 0)) || header.Uid != 0 || header.Gid != 0 {
		t.Fatalf("noncanonical header: %+v", header)
	}
	payload, err := io.ReadAll(archive)
	if err != nil || string(payload) != "tele-test" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestReleaseArgumentsAreClosed(t *testing.T) {
	for _, tc := range []struct{ version, commit, output, want string }{
		{"", "abcdef0", t.TempDir(), "release version"},
		{"latest", "abcdef0", t.TempDir(), "release version"},
		{"1.2.3", "dev", t.TempDir(), "release commit"},
		{"1.2.3", "ABCDEF0", t.TempDir(), "release commit"},
		{"1.2.3", "abcdef0", "", "output directory"},
	} {
		err := run(tc.version, tc.commit, tc.output)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("version=%q commit=%q output=%q err=%v", tc.version, tc.commit, tc.output, err)
		}
	}
}
