//go:build darwin && signingintegration

package buildtrust

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const integrationIdentity = "Developer ID Application: ARDA SEVINC (J3S8HNBXSU)"

func TestDeveloperIDCandidatePolicyRealLifecycle(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	official := copySigningFixture(t, self, "official")
	signFixture(t, official, Identifier, integrationIdentity)
	if err := VerifyOfficialPath(official); err != nil {
		t.Fatalf("official static policy: %v", err)
	}
	if err := VerifyOfficialDistribution(official); !errors.Is(err, ErrNotNotarized) {
		t.Fatalf("fresh unnotarized candidate = %v", err)
	}

	wrongIdentifier := copySigningFixture(t, self, "wrong-identifier")
	signFixture(t, wrongIdentifier, Identifier+".wrong", integrationIdentity)
	if err := VerifyOfficialPath(wrongIdentifier); !errors.Is(err, ErrNotOfficial) {
		t.Fatalf("wrong identifier candidate = %v", err)
	}

	altered := copySigningFixture(t, self, "altered")
	signFixture(t, altered, Identifier, integrationIdentity)
	file, err := os.OpenFile(altered, os.O_RDWR, 0) // #nosec G304 -- test-owned fixture path.
	if err != nil {
		t.Fatal(err)
	}
	byteAtOffset := []byte{0}
	if _, err := file.ReadAt(byteAtOffset, 4096); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	byteAtOffset[0] ^= 0xff
	if _, err := file.WriteAt(byteAtOffset, 4096); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOfficialPath(altered); !errors.Is(err, ErrNotOfficial) {
		t.Fatalf("altered candidate = %v", err)
	}
}

func TestNotarizedFixtureSatisfiesDistributionPolicy(t *testing.T) {
	fixture := os.Getenv("TELE_NOTARIZED_FIXTURE")
	if fixture == "" {
		t.Skip("TELE_NOTARIZED_FIXTURE is not set")
	}
	if err := VerifyOfficialDistribution(fixture); err != nil {
		t.Fatalf("notarized distribution fixture: %v", err)
	}
}

func copySigningFixture(t *testing.T, source, name string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), name)
	data, err := os.ReadFile(source) // #nosec G304 -- Go supplied its own test executable path.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return target
}

func signFixture(t *testing.T, path, identifier, identity string) {
	t.Helper()
	output, err := exec.Command("/usr/bin/codesign", "--force", "--sign", identity, "--identifier", identifier, "--options", "runtime", "--timestamp", path).CombinedOutput() // #nosec G204 -- test values are fixed and paths are test-owned.
	if err != nil {
		t.Fatalf("sign %s: %v: %s", path, err, output)
	}
}
