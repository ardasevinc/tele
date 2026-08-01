//go:build darwin && keychainintegration

package cli

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/session"
	"github.com/ardasevinc/tele/internal/telegram"
)

func TestLegacyKeychainMigrationRealLifecycle(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := fmt.Sprintf("legacy-migration-%d", time.Now().UnixNano())
	configPath := filepath.Join(home, "config", "tele.toml")
	if err := config.Save(configPath, config.Config{
		DefaultProfile: profile,
		Profiles:       map[string]config.Profile{profile: {}},
	}); err != nil {
		t.Fatal(err)
	}
	legacy := secrets.NewStore()
	values := map[string][]byte{
		telegram.APIHashSecretKey:     []byte("legacy-api-secret"),
		telegram.AuthPendingSecretKey: []byte(`{"phone":"+15555550123","phone_code_hash":"hash","created_at":"2026-08-01T00:00:00Z"}`),
		session.EncryptionKey:         []byte("legacy-session-secret"),
	}
	for key, value := range values {
		if err := legacy.Set(ctx, profile, key, value); err != nil {
			t.Fatal(err)
		}
		key := key
		t.Cleanup(func() { _ = legacy.Delete(ctx, profile, key) })
	}

	dataRoot := filepath.Join(home, ".local", "share", "tele")
	inventory := botstore.New(dataRoot, profile)
	state := &appState{
		cfgPath:      configPath,
		profile:      profile,
		botInventory: &inventory,
	}
	receipt, err := state.migrateSecrets(ctx, secrets.BackendKeychain)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Source.Backend != secrets.BackendKeychainLegacy ||
		receipt.Source.Instance != "" ||
		receipt.Target.Backend != secrets.BackendKeychain ||
		receipt.Target.Instance == "" ||
		receipt.KeyCount != len(values) {
		t.Fatalf("receipt = %+v", receipt)
	}
	t.Cleanup(func() {
		_, _ = secrets.PurgeKeychain(ctx, dataRoot, profile, receipt.Target.Instance)
	})
	target, err := secrets.OpenKeychain(ctx, dataRoot, profile, receipt.Target.Instance)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	snapshot, err := target.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroSnapshot(snapshot)
	for key, want := range values {
		if !bytes.Equal(snapshot[key], want) {
			t.Fatalf("target key %q mismatch", key)
		}
		retained, err := legacy.Get(ctx, profile, key)
		if err != nil || !bytes.Equal(retained, want) {
			t.Fatalf("retained source key %q mismatch: %v", key, err)
		}
		zeroSecret(retained)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	selected := cfg.Profiles[profile].Secrets
	if selected == nil || selected.Backend != string(secrets.BackendKeychain) || selected.Instance != receipt.Target.Instance {
		t.Fatalf("active selector = %+v", selected)
	}
}
