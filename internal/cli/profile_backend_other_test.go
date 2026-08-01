//go:build !darwin

package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ardasevinc/tele/internal/config"
)

func TestUseProfileLeavesNonDarwinBackendExplicitlyUnconfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	state := &appState{cfgPath: configPath}
	if err := state.useProfile(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultProfile != "work" || cfg.Profiles["work"].Secrets != nil {
		t.Fatalf("config = %+v", cfg)
	}
}
