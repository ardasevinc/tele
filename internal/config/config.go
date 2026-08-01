package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ardasevinc/tele/internal/privatefs"
)

const DefaultProfile = "default"

var profileNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Config struct {
	APIID          int64              `toml:"api_id,omitempty"`
	DefaultLimit   int                `toml:"default_limit,omitempty"`
	DefaultProfile string             `toml:"default_profile,omitempty"`
	Profiles       map[string]Profile `toml:"profiles,omitempty"`
}

type Profile struct {
	APIID   int64          `toml:"api_id,omitempty"`
	Phone   string         `toml:"phone,omitempty"`
	Secrets *SecretBackend `toml:"secrets,omitempty"`
}

type SecretBackend struct {
	Backend  string `toml:"backend,omitempty"`
	Instance string `toml:"instance,omitempty"`
}

type Paths struct {
	Config  string
	Data    string
	Runtime string
}

type PathConflict struct {
	Kind   string
	Legacy string
	XDG    string
}

type PathConflictError struct {
	Preferred Paths
	Conflicts []PathConflict
}

func (e *PathConflictError) Error() string {
	parts := make([]string, 0, len(e.Conflicts))
	for _, conflict := range e.Conflicts {
		parts = append(parts, fmt.Sprintf("%s exists in both %s and %s", conflict.Kind, conflict.Legacy, conflict.XDG))
	}
	return "legacy and XDG Tele state conflict: " + strings.Join(parts, "; ") + "; move one copy aside or unset the matching XDG variable"
}

func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	if runtime.GOOS == "linux" {
		return resolveLinuxPaths(home, os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_RUNTIME_DIR"))
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, err
	}
	return Paths{Config: filepath.Join(cfg, "tele", "config.toml"), Data: filepath.Join(home, ".local", "share", "tele")}, nil
}

func resolveLinuxPaths(home, configHome, dataHome, runtimeDir string) (Paths, error) {
	legacy := Paths{
		Config: filepath.Join(home, ".config", "tele", "config.toml"),
		Data:   filepath.Join(home, ".local", "share", "tele"),
	}
	preferred := legacy
	if filepath.IsAbs(configHome) {
		preferred.Config = filepath.Join(configHome, "tele", "config.toml")
	}
	if filepath.IsAbs(dataHome) {
		preferred.Data = filepath.Join(dataHome, "tele")
	}
	if filepath.IsAbs(runtimeDir) {
		preferred.Runtime = filepath.Join(runtimeDir, "tele")
	}

	selected := preferred
	var conflicts []PathConflict
	configPath, conflict, err := chooseStatePath("config", filepath.Dir(legacy.Config), filepath.Dir(preferred.Config))
	if err != nil {
		return Paths{}, err
	}
	if conflict != nil {
		conflicts = append(conflicts, *conflict)
	} else {
		selected.Config = filepath.Join(configPath, "config.toml")
	}
	dataPath, conflict, err := chooseStatePath("data", legacy.Data, preferred.Data)
	if err != nil {
		return Paths{}, err
	}
	if conflict != nil {
		conflicts = append(conflicts, *conflict)
	} else {
		selected.Data = dataPath
	}
	if len(conflicts) > 0 {
		return Paths{}, &PathConflictError{Preferred: preferred, Conflicts: conflicts}
	}
	return selected, nil
}

func chooseStatePath(kind, legacy, xdg string) (string, *PathConflict, error) {
	if filepath.Clean(legacy) == filepath.Clean(xdg) {
		return xdg, nil, nil
	}
	legacyPresent, err := directoryHasState(legacy)
	if err != nil {
		return "", nil, err
	}
	xdgPresent, err := directoryHasState(xdg)
	if err != nil {
		return "", nil, err
	}
	if legacyPresent && xdgPresent {
		return "", &PathConflict{Kind: kind, Legacy: legacy, XDG: xdg}, nil
	}
	if legacyPresent {
		return legacy, nil, nil
	}
	return xdg, nil, nil
}

func directoryHasState(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

func Load(path string) (Config, error) {
	var cfg Config
	if path == "" {
		paths, err := DefaultPaths()
		if err != nil {
			return cfg, err
		}
		path = paths.Config
	}
	if err := privatefs.RepairFile(path); err != nil {
		return cfg, err
	}
	// #nosec G304 -- local CLI intentionally reads an explicit user config path.
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg.DefaultLimit = 50
		cfg.DefaultProfile = DefaultProfile
		cfg.Profiles = map[string]Profile{}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	cfg, err = Parse(b)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

func Parse(b []byte) (Config, error) {
	var cfg Config
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if cfg.DefaultLimit <= 0 {
		cfg.DefaultLimit = 50
	}
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = DefaultProfile
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if path == "" {
		paths, err := DefaultPaths()
		if err != nil {
			return err
		}
		path = paths.Config
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return privatefs.AtomicWriteFile(path, b)
}

func Update(ctx context.Context, path string, update func(*Config) error) error {
	if path == "" {
		paths, err := DefaultPaths()
		if err != nil {
			return err
		}
		path = paths.Config
	}
	return privatefs.WithLock(ctx, path+".lock", func() error {
		cfg, err := Load(path)
		if err != nil {
			return err
		}
		if err := update(&cfg); err != nil {
			return err
		}
		return Save(path, cfg)
	})
}

func ValidateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	if !profileNameRE.MatchString(name) {
		return fmt.Errorf("profile %q must contain only letters, numbers, dot, underscore, or dash", name)
	}
	return nil
}

func (c Config) ResolveProfile(name string) (string, Profile, error) {
	if name == "" {
		name = c.DefaultProfile
	}
	if name == "" {
		name = DefaultProfile
	}
	if err := ValidateProfileName(name); err != nil {
		return "", Profile{}, err
	}
	profile := c.Profiles[name]
	if profile.APIID == 0 {
		profile.APIID = c.APIID
	}
	return name, profile, nil
}

func (c *Config) EnsureProfile(name string) (Profile, error) {
	if err := ValidateProfileName(name); err != nil {
		return Profile{}, err
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	profile := c.Profiles[name]
	c.Profiles[name] = profile
	if c.DefaultProfile == "" {
		c.DefaultProfile = name
	}
	return profile, nil
}

func CheckFileMode(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is readable by group/other; run chmod 600 %s", path, path)
	}
	return nil
}
