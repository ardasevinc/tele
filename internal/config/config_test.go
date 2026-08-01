package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultLimit != 50 {
		t.Fatalf("DefaultLimit = %d, want 50", cfg.DefaultLimit)
	}
	if cfg.DefaultProfile != DefaultProfile {
		t.Fatalf("DefaultProfile = %q, want %q", cfg.DefaultProfile, DefaultProfile)
	}
}

func TestDefaultPathsRespectsXDGDataHomeOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux XDG contract")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataHome := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.Data != filepath.Join(dataHome, "tele") {
		t.Fatalf("Data = %q", paths.Data)
	}
}

func TestDefaultPathsIgnoresRelativeXDGDataHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux XDG contract")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "relative")
	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(paths.Data, "relative") {
		t.Fatalf("relative XDG_DATA_HOME was accepted: %q", paths.Data)
	}
}

func TestDefaultPathsPreservesLegacyLinuxState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux XDG contract")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	legacyConfigDir := filepath.Join(home, ".config", "tele")
	legacyDataDir := filepath.Join(home, ".local", "share", "tele")
	if err := os.MkdirAll(legacyConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyConfigDir, "config.toml"), []byte("default_profile = 'main'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyDataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDataDir, "state"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.Config != filepath.Join(legacyConfigDir, "config.toml") || paths.Data != legacyDataDir {
		t.Fatalf("paths = %+v, want preserved legacy roots", paths)
	}
}

func TestDefaultPathsRejectsLegacyXDGConflict(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux XDG contract")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	configHome := filepath.Join(home, "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	for _, dir := range []string{filepath.Join(home, ".config", "tele"), filepath.Join(configHome, "tele")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("default_profile = 'main'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := DefaultPaths()
	var conflict *PathConflictError
	if !errors.As(err, &conflict) || len(conflict.Conflicts) != 1 || conflict.Conflicts[0].Kind != "config" {
		t.Fatalf("DefaultPaths error = %#v", err)
	}
	if !strings.Contains(err.Error(), "move one copy aside") {
		t.Fatalf("conflict lacks reconciliation instructions: %v", err)
	}
}

func TestDefaultPathsExposesAbsoluteRuntimeDir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux XDG contract")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	runtimeDir := filepath.Join(home, "runtime")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.Runtime != filepath.Join(runtimeDir, "tele") {
		t.Fatalf("Runtime = %q", paths.Runtime)
	}
}

func TestLoadRejectsCorruptConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[profiles\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted corrupt TOML")
	}
}

func TestUpdateSerializesConcurrentConfigMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("profile-%02d", i)
			errs <- Update(context.Background(), path, func(cfg *Config) error {
				_, err := cfg.EnsureProfile(name)
				return err
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != workers {
		t.Fatalf("profiles = %d, want %d", len(cfg.Profiles), workers)
	}
}

func TestSaveUsesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, Config{DefaultProfile: "test", Profiles: map[string]Profile{"test": {APIID: 123}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestLoadRepairsExistingPrivateModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tele")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("default_profile = 'main'\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", target, got, want)
		}
	}
}

func TestResolveProfileFallsBackToRootAPIID(t *testing.T) {
	cfg := Config{
		APIID:          123,
		DefaultProfile: "test",
		Profiles:       map[string]Profile{"test": {}},
	}
	name, profile, err := cfg.ResolveProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if name != "test" {
		t.Fatalf("name = %q, want test", name)
	}
	if profile.APIID != 123 {
		t.Fatalf("APIID = %d, want 123", profile.APIID)
	}
}

func TestSecretBackendConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want := Config{
		DefaultProfile: "main",
		Profiles: map[string]Profile{
			"main": {
				APIID: 123,
				Secrets: &SecretBackend{
					Backend:  "vault-v1",
					Instance: "8e34c2c8-9c20-4cb4-ae66-e63ee0f3be50",
				},
			},
		},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if *got.Profiles["main"].Secrets != *want.Profiles["main"].Secrets {
		t.Fatalf("secrets = %+v, want %+v", got.Profiles["main"].Secrets, want.Profiles["main"].Secrets)
	}
}

func TestSaveOmitsUnconfiguredSecretBackend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, Config{Profiles: map[string]Profile{"main": {APIID: 123}}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secrets") {
		t.Fatalf("unconfigured secret backend was serialized:\n%s", b)
	}
}

func TestValidateProfileName(t *testing.T) {
	valid := []string{"default", "test_1", "main.work"}
	for _, name := range valid {
		if err := ValidateProfileName(name); err != nil {
			t.Fatalf("ValidateProfileName(%q): %v", name, err)
		}
	}
	invalid := []string{"", "../bad", "bad/name", "bad name"}
	for _, name := range invalid {
		if err := ValidateProfileName(name); err == nil {
			t.Fatalf("ValidateProfileName(%q) succeeded, want error", name)
		}
	}
}
