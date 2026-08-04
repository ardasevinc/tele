package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

type target struct{ os, arch string }

var releaseTargets = []target{
	{"darwin", "amd64"},
	{"darwin", "arm64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
}

func main() {
	var version, commit, output, binaryInput, binaryOutput string
	flag.StringVar(&version, "version", "", "release version without the v prefix")
	flag.StringVar(&commit, "commit", "", "source commit SHA")
	flag.StringVar(&output, "output", "dist", "artifact output directory")
	flag.StringVar(&binaryInput, "binary-input", "", "package prebuilt target binaries from this directory")
	flag.StringVar(&binaryOutput, "binary-output", "", "retain freshly built target binaries in this directory")
	flag.Parse()
	if err := run(version, commit, output, binaryInput, binaryOutput); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(version, commit, output, binaryInput, binaryOutput string) error {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	commit = strings.TrimSpace(commit)
	if !versionPattern.MatchString(version) {
		return errors.New("release version must be semantic and omit the v prefix")
	}
	if !commitPattern.MatchString(commit) {
		return errors.New("release commit must be a 7-40 character lowercase hex SHA")
	}
	if strings.TrimSpace(output) == "" {
		return errors.New("release output directory is required")
	}
	if binaryInput != "" && binaryOutput != "" {
		return errors.New("binary input and binary output are mutually exclusive")
	}
	if err := os.MkdirAll(output, 0o755); err != nil { // #nosec G301 -- release artifacts are intentionally public-readable.
		return err
	}
	tmp, err := os.MkdirTemp("", "tele-release-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	checksums := make([]string, 0, len(releaseTargets))
	for _, target := range releaseTargets {
		binaryName := fmt.Sprintf("tele_%s_%s", target.os, target.arch)
		binary := filepath.Join(tmp, binaryName)
		if binaryInput != "" {
			binary = filepath.Join(binaryInput, binaryName)
			if err := validateBinaryInput(binary); err != nil {
				return fmt.Errorf("prebuilt %s/%s: %w", target.os, target.arch, err)
			}
		} else {
			if err := buildBinary(binary, version, commit, target); err != nil {
				return err
			}
			if binaryOutput != "" {
				retained := filepath.Join(binaryOutput, binaryName)
				if err := copyBinary(binary, retained); err != nil {
					return fmt.Errorf("retain %s/%s binary: %w", target.os, target.arch, err)
				}
			}
		}
		name := fmt.Sprintf("tele_%s_%s_%s.tar.gz", version, target.os, target.arch)
		archivePath := filepath.Join(output, name)
		if err := writeArchive(archivePath, binary, "LICENSE"); err != nil {
			return fmt.Errorf("package %s/%s: %w", target.os, target.arch, err)
		}
		digest, err := fileDigest(archivePath)
		if err != nil {
			return err
		}
		checksums = append(checksums, digest+"  "+name)
	}
	sort.Strings(checksums)
	return os.WriteFile(filepath.Join(output, "checksums.txt"), []byte(strings.Join(checksums, "\n")+"\n"), 0o644) // #nosec G306 -- release checksums are intentionally public-readable.
}

func buildBinary(path, version, commit string, target target) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- temporary release inputs contain no secrets.
		return err
	}
	ldflags := fmt.Sprintf("-s -w -buildid= -X github.com/ardasevinc/tele/internal/buildinfo.Version=%s -X github.com/ardasevinc/tele/internal/buildinfo.Commit=%s", version, commit)
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", path, "./cmd/tele") // #nosec G204 -- variables are validated or selected from a closed target list.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.os, "GOARCH="+target.arch)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build %s/%s: %w", target.os, target.arch, err)
	}
	return nil
}

func validateBinaryInput(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("input must be a regular non-symlink file")
	}
	if info.Size() == 0 {
		return errors.New("input must not be empty")
	}
	return nil
}

func copyBinary(sourcePath, targetPath string) (retErr error) {
	if err := validateBinaryInput(sourcePath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil { // #nosec G301 -- release inputs contain no secrets.
		return err
	}
	source, err := os.Open(sourcePath) // #nosec G304 -- internally selected release path.
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, source.Close()) }()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755) // #nosec G302,G304 -- executable release input.
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, target.Close()) }()
	_, err = io.Copy(target, source)
	return err
}

func writeArchive(path, binary, license string) (retErr error) {
	data, err := os.ReadFile(binary) // #nosec G304 -- binary is an internally generated release path.
	if err != nil {
		return err
	}
	licenseData, err := os.ReadFile(license) // #nosec G304 -- license is the repository-controlled release notice.
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644) // #nosec G302,G304 -- public release path and artifact.
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	zipper, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	zipper.ModTime = time.Unix(0, 0).UTC()
	zipper.OS = 255
	archive := tar.NewWriter(zipper)
	for _, entry := range []struct {
		name string
		mode int64
		data []byte
	}{
		{name: "tele", mode: 0o755, data: data},
		{name: "LICENSE", mode: 0o644, data: licenseData},
	} {
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.data)), ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if _, err := archive.Write(entry.data); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return zipper.Close()
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- path is an internally generated release artifact.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
