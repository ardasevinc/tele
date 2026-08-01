package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/session"
	"github.com/ardasevinc/tele/internal/telegram"
)

func TestSecretsInitAndAPIHashRoundTripOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux portable-vault workflow")
	}
	root := t.TempDir()
	dataHome := filepath.Join(root, "data")
	t.Setenv("HOME", filepath.Join(root, "home"))
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

	oldInstance := profile.Secrets.Instance
	oldPath := secrets.VaultPath(filepath.Join(dataHome, "tele"), "main", oldInstance)
	stdout.Reset()
	stderr.Reset()
	state = &appState{in: strings.NewReader("must not be read"), out: &stdout, err: &stderr}
	err = executeWithState(context.Background(), []string{
		"--json", "--config", configPath, "--profile", "main",
		"--vault-passphrase-file", passphrasePath,
		"secrets", "migrate", "--to", "vault-v1",
	}, state)
	if err != nil {
		t.Fatalf("secrets migrate: %v\nstderr=%s", err, stderr.String())
	}
	var migrationEnvelope struct {
		Data migrationReceipt `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &migrationEnvelope); err != nil {
		t.Fatal(err)
	}
	receipt := migrationEnvelope.Data
	if receipt.Source.Instance != oldInstance || receipt.Target.Instance == "" || receipt.Target.Instance == oldInstance || receipt.KeyCount != 1 {
		t.Fatalf("migration receipt = %+v", receipt)
	}
	cfg, err = config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	profile = cfg.Profiles["main"]
	if profile.Secrets.Instance != receipt.Target.Instance {
		t.Fatalf("active selector = %+v, receipt target = %+v", profile.Secrets, receipt.Target)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("retained source vault: %v", err)
	}
	target, err := secrets.OpenVault(
		secrets.VaultPath(filepath.Join(dataHome, "tele"), "main", receipt.Target.Instance),
		"main", receipt.Target.Instance, passphrase,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	hash, err = target.Get(context.Background(), "main", "api-hash")
	if err != nil || string(hash) != "SUPERSECRET" {
		t.Fatalf("migrated api hash = %q, %v", hash, err)
	}
	receiptPath := filepath.Join(dataHome, "tele", "main", "secrets", "migrations", receipt.Target.Instance+".json")
	if info, err := os.Stat(receiptPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("migration receipt mode = %v, %v", info, err)
	}
	purgeState := &appState{cfgPath: configPath, profile: "main"}
	preview, err := purgeState.purgeVault(context.Background(), oldInstance, "")
	if err != nil || preview.Active || preview.Purged {
		t.Fatalf("purge preview = %+v, %v", preview, err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("preview deleted source vault: %v", err)
	}
	if _, err := purgeState.purgeVault(context.Background(), receipt.Target.Instance, receipt.Target.Instance); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("active purge error = %v", err)
	}
	if _, err := purgeState.purgeVault(context.Background(), oldInstance, receipt.Target.Instance); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("mismatched confirmation error = %v", err)
	}
	purged, err := purgeState.purgeVault(context.Background(), oldInstance, oldInstance)
	if err != nil || !purged.Purged {
		t.Fatalf("purge = %+v, %v", purged, err)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged source still exists: %v", err)
	}
}

func TestVaultMachineModeRequiresExplicitPassphraseSource(t *testing.T) {
	state := &appState{json: true, vaultPassphraseFD: -1}
	if _, err := state.readVaultPassphrase(context.Background(), false); err == nil || !strings.Contains(err.Error(), "source required") {
		t.Fatalf("readVaultPassphrase error = %v", err)
	}
}

func TestVaultPassphraseSourcesAreMutuallyExclusive(t *testing.T) {
	state := &appState{vaultPassphraseFD: 3, vaultPassphraseFile: "/tmp/credential"}
	if _, err := state.readVaultPassphrase(context.Background(), false); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("readVaultPassphrase error = %v", err)
	}
}

func TestVaultMigrationSelectorFailureRetainsSourceAuthority(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	configPath := filepath.Join(root, "config", "tele.toml")
	passphrasePath := filepath.Join(root, "credential")
	passphrase := []byte("selector failure passphrase")
	if err := os.WriteFile(passphrasePath, append(append([]byte(nil), passphrase...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceInstance := "4ce21a61-83de-463a-92c2-85701457a13a"
	sourcePath := secrets.VaultPath(dataRoot, "main", sourceInstance)
	source, err := secrets.CreateVault(context.Background(), sourcePath, "main", sourceInstance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Set(context.Background(), "main", telegram.APIHashSecretKey, []byte("retained secret")); err != nil {
		t.Fatal(err)
	}
	source.Close()
	if err := config.Save(configPath, config.Config{
		DefaultProfile: "main",
		Profiles: map[string]config.Profile{
			"main": {Secrets: &config.SecretBackend{Backend: string(secrets.BackendVault), Instance: sourceInstance}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected selector failure")
	state := &appState{
		cfgPath: configPath, profile: "main", vaultPassphraseFile: passphrasePath, vaultPassphraseFD: -1,
		pathOverride: &config.Paths{Config: configPath, Data: dataRoot},
		configUpdater: func(context.Context, string, func(*config.Config) error) error {
			return injected
		},
	}
	receipt, err := state.migrateSecrets(context.Background(), secrets.BackendVault)
	if !errors.Is(err, injected) {
		t.Fatalf("migration error = %v", err)
	}
	if receipt.Target.Instance == "" || receipt.Target.Instance == sourceInstance {
		t.Fatalf("target receipt = %+v", receipt)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	selected := cfg.Profiles["main"].Secrets
	if selected == nil || selected.Instance != sourceInstance || selected.Backend != string(secrets.BackendVault) {
		t.Fatalf("failed activation changed source authority: %+v", selected)
	}
	for _, instance := range []string{sourceInstance, receipt.Target.Instance} {
		store, openErr := secrets.OpenVault(secrets.VaultPath(dataRoot, "main", instance), "main", instance, passphrase)
		if openErr != nil {
			t.Fatalf("open retained instance %s: %v", instance, openErr)
		}
		value, getErr := store.Get(context.Background(), "main", telegram.APIHashSecretKey)
		store.Close()
		if getErr != nil || string(value) != "retained secret" {
			t.Fatalf("instance %s secret = %q, %v", instance, value, getErr)
		}
	}
}

func TestLegacyMigrationSnapshotUsesFixedCatalogWithoutConfiguredBots(t *testing.T) {
	ctx := context.Background()
	store := &botCommandStore{values: map[string][]byte{
		"main:" + telegram.APIHashSecretKey: []byte("api-secret"),
		"main:" + session.EncryptionKey:     []byte("session-secret"),
	}}
	inventory := botstore.New(t.TempDir(), "main")
	discoverer := &botCommandDiscoverer{}
	state := &appState{
		secretStore:        store,
		ownedBotDiscoverer: discoverer,
		botInventory:       &inventory,
	}
	snapshot, err := state.migrationSnapshot(
		ctx,
		secrets.Selection{Backend: secrets.BackendKeychainLegacy},
		store,
		"main",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroSnapshot(snapshot)
	if len(snapshot) != 2 || string(snapshot[telegram.APIHashSecretKey]) != "api-secret" ||
		string(snapshot[session.EncryptionKey]) != "session-secret" {
		t.Fatalf("snapshot keys=%v", snapshot)
	}
	if discoverer.calls != 0 {
		t.Fatalf("remote discovery ran without a configured bot manager: %d", discoverer.calls)
	}
}

func TestMigrationProfileSelectionTracksImplicitAndExplicitSources(t *testing.T) {
	implicit := config.Profile{}
	if got := selectionFromProfile(implicit); got != (secrets.Selection{}) || !profileStillSelects(implicit, got) {
		t.Fatalf("implicit selection = %+v", got)
	}
	explicit := config.Profile{Secrets: &config.SecretBackend{Backend: string(secrets.BackendVault), Instance: "instance"}}
	selection := selectionFromProfile(explicit)
	if !profileStillSelects(explicit, selection) || profileStillSelects(config.Profile{}, selection) {
		t.Fatalf("explicit selection agreement failed: %+v", selection)
	}
}
