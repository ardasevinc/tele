package config

import (
	"os"
	"path/filepath"
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
