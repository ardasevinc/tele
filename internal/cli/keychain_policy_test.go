package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/secrets"
)

func TestUntrustedBuildRejectsConfiguredKeychainBeforeOpeningIt(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	instance := "95a82a93-9282-46af-afc8-8000299505ff"
	if err := config.Save(configPath, config.Config{
		DefaultProfile: "main",
		Profiles: map[string]config.Profile{"main": {
			Secrets: &config.SecretBackend{Backend: string(secrets.BackendKeychain), Instance: instance},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	state := &appState{
		cfgPath:               configPath,
		pathOverride:          &config.Paths{Config: configPath, Data: filepath.Join(root, "data")},
		officialKeychainCheck: func() error { return errors.New("not official") },
	}
	if _, err := state.openSecretStore(context.Background()); !errors.Is(err, secrets.ErrBackendUnavailable) || !strings.Contains(err.Error(), "vault-v1") {
		t.Fatalf("openSecretStore = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untrusted open touched data root: %v", err)
	}
}

func TestUntrustedBuildRejectsKeychainInitBeforeStateAccess(t *testing.T) {
	state := &appState{officialKeychainCheck: func() error { return errors.New("not official") }}
	if _, err := state.initKeychain(context.Background()); !errors.Is(err, secrets.ErrBackendUnavailable) {
		t.Fatalf("initKeychain = %v", err)
	}
}
