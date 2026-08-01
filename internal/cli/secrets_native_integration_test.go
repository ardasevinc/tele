//go:build (linux && secretserviceintegration) || (darwin && keychainintegration)

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/session"
	"github.com/ardasevinc/tele/internal/telegram"
)

func testVaultNativeMigrationLifecycle(
	t *testing.T,
	backend secrets.BackendID,
	openNative func(context.Context, string, string, string) (secrets.Store, error),
	purgeNative func(*appState, context.Context, string, string) (purgeResult, error),
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := "migration-" + strings.ReplaceAll(string(backend), "-", "_")
	configPath := filepath.Join(root, "config", "tele.toml")
	dataRoot := filepath.Join(root, "data")
	passphrasePath := filepath.Join(root, "vault-passphrase")
	passphrase := []byte("native migration integration passphrase")
	if err := os.WriteFile(passphrasePath, append(append([]byte(nil), passphrase...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, err := secrets.NewVaultInstance()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, config.Config{
		DefaultProfile: profile,
		Profiles: map[string]config.Profile{profile: {
			Secrets: &config.SecretBackend{Backend: string(secrets.BackendVault), Instance: instance},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	values := map[string][]byte{
		telegram.APIHashSecretKey: []byte("migration-api-secret"),
		session.EncryptionKey:     []byte("migration-session-secret"),
	}
	source, err := secrets.CreateVault(ctx, secrets.VaultPath(dataRoot, profile, instance), profile, instance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range values {
		if err := source.Set(ctx, profile, key, value); err != nil {
			t.Fatal(err)
		}
	}
	source.Close()

	state := &appState{
		cfgPath: configPath, profile: profile, vaultPassphraseFile: passphrasePath,
		vaultPassphraseFD: -1, pathOverride: &config.Paths{Config: configPath, Data: dataRoot},
	}
	toNative, err := state.migrateSecrets(ctx, backend)
	if err != nil {
		t.Fatal(err)
	}
	if toNative.Source.Instance != instance || toNative.Target.Backend != backend || toNative.KeyCount != len(values) {
		t.Fatalf("vault-to-native receipt = %+v", toNative)
	}
	t.Cleanup(func() {
		_, _ = purgeNative(state, context.Background(), toNative.Target.Instance, toNative.Target.Instance)
	})
	native, err := openNative(ctx, dataRoot, profile, toNative.Target.Instance)
	if err != nil {
		t.Fatal(err)
	}
	assertSecretSnapshot(t, ctx, native, values)
	closeSecretStore(native)

	toVault, err := state.migrateSecrets(ctx, secrets.BackendVault)
	if err != nil {
		t.Fatal(err)
	}
	if toVault.Source != toNative.Target || toVault.Target.Backend != secrets.BackendVault || toVault.KeyCount != len(values) {
		t.Fatalf("native-to-vault receipt = %+v", toVault)
	}
	restored, err := secrets.OpenVault(secrets.VaultPath(dataRoot, profile, toVault.Target.Instance), profile, toVault.Target.Instance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	assertSecretSnapshot(t, ctx, restored, values)
	restored.Close()

	purged, err := purgeNative(state, ctx, toNative.Target.Instance, toNative.Target.Instance)
	if err != nil || !purged.Purged {
		t.Fatalf("native purge = %+v, %v", purged, err)
	}
	retained, err := secrets.OpenVault(secrets.VaultPath(dataRoot, profile, instance), profile, instance, passphrase)
	if err != nil {
		t.Fatalf("retained original vault: %v", err)
	}
	retained.Close()
}

func assertSecretSnapshot(t *testing.T, ctx context.Context, store secrets.Store, want map[string][]byte) {
	t.Helper()
	snapshotter, ok := store.(secrets.Snapshotter)
	if !ok {
		t.Fatal("native store has no authoritative snapshot")
	}
	snapshot, err := snapshotter.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroSnapshot(snapshot)
	if len(snapshot) != len(want) {
		t.Fatalf("snapshot has %d records, want %d", len(snapshot), len(want))
	}
	for key, value := range want {
		if !bytes.Equal(snapshot[key], value) {
			t.Fatalf("snapshot key %q mismatch", key)
		}
	}
}
