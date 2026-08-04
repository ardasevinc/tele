//go:build darwin

package buildtrust

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

var (
	verifyOnce sync.Once
	verifyErr  error
)

func VerifyOfficial() error {
	verifyOnce.Do(func() {
		verifyErr = verifyOfficial()
	})
	return verifyErr
}

func verifyOfficial() error {
	return verifyCode("")
}

func VerifyOfficialPath(path string) error {
	return verifyCode(path)
}

func VerifyOfficialDistribution(path string) error {
	if err := VerifyOfficialPath(path); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "--check-notarization", "-R=notarized", "--verbose=2", path).CombinedOutput() // #nosec G204 -- path is passed as one argument to Apple's verifier.
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotNotarized, string(output))
	}
	return nil
}

func verifyCode(path string) error {
	coreFoundation, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("load CoreFoundation: %w", err)
	}
	defer func() { _ = purego.Dlclose(coreFoundation) }()
	security, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("load Security.framework: %w", err)
	}
	defer func() { _ = purego.Dlclose(security) }()

	var cfRelease func(uintptr)
	var cfStringCreate func(uintptr, string, uint32) uintptr
	var cfURLCreateFromFileSystemRepresentation func(uintptr, *byte, int64, uint8) uintptr
	var secCodeCopySelf func(uint32, *uintptr) int32
	var secStaticCodeCreateWithPath func(uintptr, uint32, *uintptr) int32
	var secRequirementCreateWithString func(uintptr, uint32, *uintptr) int32
	var secCodeCheckValidity func(uintptr, uint32, uintptr) int32
	bindings := []struct {
		handle uintptr
		name   string
		target any
	}{
		{coreFoundation, "CFRelease", &cfRelease},
		{coreFoundation, "CFStringCreateWithCString", &cfStringCreate},
		{coreFoundation, "CFURLCreateFromFileSystemRepresentation", &cfURLCreateFromFileSystemRepresentation},
		{security, "SecCodeCopySelf", &secCodeCopySelf},
		{security, "SecStaticCodeCreateWithPath", &secStaticCodeCreateWithPath},
		{security, "SecRequirementCreateWithString", &secRequirementCreateWithString},
		{security, "SecCodeCheckValidity", &secCodeCheckValidity},
	}
	for _, binding := range bindings {
		symbol, err := purego.Dlsym(binding.handle, binding.name)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", binding.name, err)
		}
		purego.RegisterFunc(binding.target, symbol)
	}

	const utf8Encoding = uint32(0x08000100)
	requirementText := cfStringCreate(0, Requirement, utf8Encoding)
	if requirementText == 0 {
		return fmt.Errorf("create official signing requirement text")
	}
	defer cfRelease(requirementText)

	var requirement uintptr
	if status := secRequirementCreateWithString(requirementText, 0, &requirement); status != 0 {
		return fmt.Errorf("compile official signing requirement: security framework status %d", status)
	}
	defer cfRelease(requirement)

	var code uintptr
	if path == "" {
		if status := secCodeCopySelf(0, &code); status != 0 {
			return fmt.Errorf("inspect running code identity: security framework status %d", status)
		}
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve candidate path: %w", err)
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			return fmt.Errorf("inspect candidate: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: candidate is not a regular non-symlink file", ErrNotOfficial)
		}
		pathBytes := []byte(absolute)
		url := cfURLCreateFromFileSystemRepresentation(0, unsafe.SliceData(pathBytes), int64(len(pathBytes)), 0) // #nosec G103 -- CoreFoundation copies this live byte slice synchronously.
		if url == 0 {
			return fmt.Errorf("create candidate URL")
		}
		defer cfRelease(url)
		if status := secStaticCodeCreateWithPath(url, 0, &code); status != 0 {
			return fmt.Errorf("%w: create static code: security framework status %d", ErrNotOfficial, status)
		}
	}
	defer cfRelease(code)

	const checkAllArchitecturesAndStrict = uint32(1<<0 | 1<<4)
	if status := secCodeCheckValidity(code, checkAllArchitecturesAndStrict, requirement); status != 0 {
		return fmt.Errorf("%w: security framework status %d", ErrNotOfficial, status)
	}
	return nil
}
