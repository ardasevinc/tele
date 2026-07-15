package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
