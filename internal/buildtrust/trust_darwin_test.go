//go:build darwin

package buildtrust

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUnsignedTestBinaryIsNotOfficial(t *testing.T) {
	if err := verifyOfficial(); !errors.Is(err, ErrNotOfficial) {
		t.Fatalf("verifyOfficial = %v, want ErrNotOfficial", err)
	}
}

func TestUnsignedAndAdHocPathsAreNotOfficial(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOfficialPath(self); !errors.Is(err, ErrNotOfficial) {
		t.Fatalf("unsigned test binary verification = %v", err)
	}

	fixture := filepath.Join(t.TempDir(), "tele")
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, data, 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--identifier", Identifier, fixture).CombinedOutput()
	if err != nil {
		t.Fatalf("ad-hoc sign fixture: %v: %s", err, output)
	}
	if err := VerifyOfficialPath(fixture); !errors.Is(err, ErrNotOfficial) {
		t.Fatalf("ad-hoc fixture verification = %v", err)
	}
	if err := VerifyOfficialDistribution(fixture); !errors.Is(err, ErrNotOfficial) {
		t.Fatalf("ad-hoc distribution verification = %v", err)
	}
}
