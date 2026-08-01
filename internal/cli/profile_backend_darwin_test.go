//go:build darwin

package cli

import (
	"testing"

	"github.com/ardasevinc/tele/internal/secrets"
)

func TestNewDarwinProfilesSelectNativeKeychain(t *testing.T) {
	if newProfileBackend() != secrets.BackendKeychain {
		t.Fatalf("newProfileBackend = %q", newProfileBackend())
	}
}
