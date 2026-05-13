package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/pelletier/go-toml/v2"
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
	APIID int64  `toml:"api_id,omitempty"`
	Phone string `toml:"phone,omitempty"`
}

type Paths struct {
	Config string
	Data   string
}

func DefaultPaths() (Paths, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, err
	}
	data, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Config: filepath.Join(cfg, "tele", "config.toml"),
		Data:   filepath.Join(data, ".local", "share", "tele"),
	}, nil
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
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
