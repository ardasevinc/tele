package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/secrets"
)

func TestSecretsInitAndAPIHashRoundTripOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux portable-vault workflow")
	}
	root := t.TempDir()
	dataHome := filepath.Join(root, "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	configPath := filepath.Join(root, "config", "tele.toml")
	passphrasePath := filepath.Join(root, "vault-passphrase")
	if err := os.WriteFile(passphrasePath, []byte("test passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	state := &appState{in: strings.NewReader("must not be read"), out: &stdout, err: &stderr}
	err := executeWithState(context.Background(), []string{
		"--json", "--config", configPath, "--profile", "main",
		"--vault-passphrase-file", passphrasePath,
		"secrets", "init", "--backend", "vault-v1",
	}, state)
	if err != nil {
		t.Fatalf("secrets init: %v\nstderr=%s", err, stderr.String())
	}
	var envelope struct {
		Data secretInitResult `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Backend != secrets.BackendVault || envelope.Data.Profile != "main" || envelope.Data.Instance == "" {
		t.Fatalf("init result = %+v", envelope.Data)
	}

	t.Setenv("TELE_TEST_API_HASH", "SUPERSECRET")
	stdout.Reset()
	stderr.Reset()
	state = &appState{in: strings.NewReader("must not be read"), out: &stdout, err: &stderr}
	err = executeWithState(context.Background(), []string{
		"--json", "--config", configPath, "--profile", "main",
		"--vault-passphrase-file", passphrasePath,
		"config", "set", "api-hash", "--value-env", "TELE_TEST_API_HASH",
	}, state)
	if err != nil {
		t.Fatalf("config set api-hash: %v\nstderr=%s", err, stderr.String())
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.Profiles["main"]
	if profile.Secrets == nil || profile.Secrets.Backend != string(secrets.BackendVault) || profile.Secrets.Instance != envelope.Data.Instance {
		t.Fatalf("persisted selector = %+v", profile.Secrets)
	}
	passphrase, err := secrets.ReadPassphraseFile(passphrasePath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := secrets.OpenVault(
		secrets.VaultPath(filepath.Join(dataHome, "tele"), "main", profile.Secrets.Instance),
		"main", profile.Secrets.Instance, passphrase,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hash, err := store.Get(context.Background(), "main", "api-hash")
	if err != nil || string(hash) != "SUPERSECRET" {
		t.Fatalf("stored api hash = %q, %v", hash, err)
	}
}

func TestVaultMachineModeRequiresExplicitPassphraseSource(t *testing.T) {
	state := &appState{json: true, vaultPassphraseFD: -1}
	if _, err := state.readVaultPassphrase(false); err == nil || !strings.Contains(err.Error(), "source required") {
		t.Fatalf("readVaultPassphrase error = %v", err)
	}
}

func TestVaultPassphraseSourcesAreMutuallyExclusive(t *testing.T) {
	state := &appState{vaultPassphraseFD: 3, vaultPassphraseFile: "/tmp/credential"}
	if _, err := state.readVaultPassphrase(false); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("readVaultPassphrase error = %v", err)
	}
}
