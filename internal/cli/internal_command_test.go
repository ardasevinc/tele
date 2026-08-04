package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/secrets"
)

func TestInternalOfficialBuildUsesCryptographicPolicy(t *testing.T) {
	var stdout bytes.Buffer
	state := &appState{
		out: &stdout, err: &bytes.Buffer{},
		officialKeychainCheck: func() error { return errors.New("not official") },
	}
	if err := executeWithState(context.Background(), []string{"internal", "official-build"}, state); err == nil {
		t.Fatal("untrusted build reported official")
	}

	stdout.Reset()
	state = &appState{
		out: &stdout, err: &bytes.Buffer{},
		officialKeychainCheck: func() error { return nil },
	}
	if err := executeWithState(context.Background(), []string{"internal", "official-build"}, state); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "tele-official-build-v1\n" {
		t.Fatalf("official-build output = %q", stdout.String())
	}
}

func TestInternalCompatibilityChecksCurrentConfigAndVaultWithoutUnlocking(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config", "tele.toml")
	dataRoot := filepath.Join(root, "data")
	instance, err := secrets.NewVaultInstance()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, config.Config{
		DefaultProfile: "main",
		Profiles: map[string]config.Profile{"main": {
			Secrets: &config.SecretBackend{Backend: string(secrets.BackendVault), Instance: instance},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	vaultPath := secrets.VaultPath(dataRoot, "main", instance)
	vault, err := secrets.CreateVault(context.Background(), vaultPath, "main", instance, []byte("compatibility test passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	vault.Close()
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	vaultBefore, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	state := &appState{
		in: bytes.NewReader(nil), out: &stdout, err: &stderr, pathOverride: &config.Paths{Config: configPath, Data: dataRoot},
	}
	if err := executeWithState(context.Background(), []string{"internal", "compatibility"}, state); err != nil {
		t.Fatalf("compatibility: %v stderr=%s", err, stderr.String())
	}
	if stdout.String() != "tele-compatible-v1\n" {
		t.Fatalf("compatibility output = %q", stdout.String())
	}
	configAfter, configErr := os.ReadFile(configPath)
	vaultAfter, vaultErr := os.ReadFile(vaultPath)
	if configErr != nil || vaultErr != nil || !bytes.Equal(configBefore, configAfter) || !bytes.Equal(vaultBefore, vaultAfter) {
		t.Fatalf("compatibility preflight mutated state: config_err=%v vault_err=%v", configErr, vaultErr)
	}

	if err := os.WriteFile(vaultPath, []byte("future-or-corrupt-vault"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.checkCompatibility(); err == nil {
		t.Fatal("compatibility accepted unreadable current vault format")
	}
}

func TestInternalCompatibilityAcceptsMissingConfig(t *testing.T) {
	root := t.TempDir()
	state := &appState{pathOverride: &config.Paths{
		Config: filepath.Join(root, "missing.toml"), Data: filepath.Join(root, "data"),
	}}
	if err := state.checkCompatibility(); err != nil {
		t.Fatal(err)
	}
}
