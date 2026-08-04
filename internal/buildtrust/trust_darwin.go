//go:build darwin

package buildtrust

import (
	"fmt"
	"sync"

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
	var secCodeCopySelf func(uint32, *uintptr) int32
	var secRequirementCreateWithString func(uintptr, uint32, *uintptr) int32
	var secCodeCheckValidity func(uintptr, uint32, uintptr) int32
	bindings := []struct {
		handle uintptr
		name   string
		target any
	}{
		{coreFoundation, "CFRelease", &cfRelease},
		{coreFoundation, "CFStringCreateWithCString", &cfStringCreate},
		{security, "SecCodeCopySelf", &secCodeCopySelf},
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

	var self uintptr
	if status := secCodeCopySelf(0, &self); status != 0 {
		return fmt.Errorf("inspect running code identity: security framework status %d", status)
	}
	defer cfRelease(self)

	if status := secCodeCheckValidity(self, 0, requirement); status != 0 {
		return fmt.Errorf("%w: security framework status %d", ErrNotOfficial, status)
	}
	return nil
}
