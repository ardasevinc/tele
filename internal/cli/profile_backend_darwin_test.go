//go:build darwin

package cli

import (
	"errors"
	"testing"

	"github.com/ardasevinc/tele/internal/secrets"
)

func TestNewDarwinProfilesSelectNativeKeychainOnlyForOfficialBuild(t *testing.T) {
	untrusted := &appState{officialKeychainCheck: func() error { return errors.New("not official") }}
	if untrusted.newProfileBackend() != "" {
		t.Fatalf("untrusted newProfileBackend = %q", untrusted.newProfileBackend())
	}
	official := &appState{officialKeychainCheck: func() error { return nil }}
	if official.newProfileBackend() != secrets.BackendKeychain {
		t.Fatalf("official newProfileBackend = %q", official.newProfileBackend())
	}
}
