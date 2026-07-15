package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/peerstore"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/session"
)

type Status string

const (
	Pass    Status = "pass"
	Warning Status = "warning"
	Fail    Status = "failed"
	Skipped Status = "skipped"
)

type Check struct {
	Name    string         `json:"name"`
	Status  Status         `json:"status"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type Report struct {
	OK            bool    `json:"ok"`
	Version       string  `json:"version"`
	Commit        string  `json:"commit"`
	Profile       string  `json:"profile"`
	ConfigPath    string  `json:"config_path"`
	DataPath      string  `json:"data_path"`
	Executable    string  `json:"executable,omitempty"`
	InstalledPath string  `json:"installed_path,omitempty"`
	Checks        []Check `json:"checks"`
}

type Options struct {
	Paths                  config.Paths
	Profile                string
	Secrets                secrets.Store
	SecretBackend          string
	SecretBackendSupported bool
	Version                string
	Commit                 string
	Connect                bool
	Probe                  func(context.Context) (authorized bool, err error)
	Executable             string
	InstalledPath          string
}

func Run(ctx context.Context, opts Options) Report {
	report := Report{
		Version:       opts.Version,
		Commit:        opts.Commit,
		ConfigPath:    opts.Paths.Config,
		DataPath:      opts.Paths.Data,
		Executable:    cleanPath(opts.Executable),
		InstalledPath: cleanPath(opts.InstalledPath),
		Checks:        make([]Check, 0, 13),
	}
	if report.Executable == "" {
		report.Executable, _ = os.Executable()
		report.Executable = cleanPath(report.Executable)
	}
	if report.InstalledPath == "" {
		report.InstalledPath, _ = exec.LookPath("tele")
		report.InstalledPath = cleanPath(report.InstalledPath)
	}

	cfg, configReady := inspectConfig(opts.Paths.Config, &report)
	profileName := opts.Profile
	if profileName == "" && configReady {
		profileName = cfg.DefaultProfile
	}
	if profileName == "" {
		profileName = config.DefaultProfile
	}
	report.Profile = profileName

	var profile config.Profile
	profileReady := false
	if !configReady {
		report.add("profile", Skipped, "config is unavailable", nil)
	} else if err := config.ValidateProfileName(profileName); err != nil {
		report.add("profile", Fail, "profile name is invalid", nil)
	} else {
		_, profile, _ = cfg.ResolveProfile(profileName)
		profileReady = true
		report.add("profile", Pass, "profile resolved", map[string]any{"name": profileName})
	}
	if !profileReady {
		report.add("api_id", Skipped, "profile is unavailable", nil)
	} else if profile.APIID <= 0 {
		report.add("api_id", Fail, "API ID is missing", nil)
	} else {
		report.add("api_id", Pass, "API ID is configured", nil)
	}

	if opts.SecretBackendSupported {
		report.add("secret_store", Pass, "secret store is supported", map[string]any{"backend": opts.SecretBackend})
	} else {
		report.add("secret_store", Fail, "secret store is unsupported", map[string]any{"backend": opts.SecretBackend})
	}
	apiHashReady := inspectSecret(ctx, opts, profileName, "api-hash", "api_hash", &report)
	sessionKeyReady := inspectSecret(ctx, opts, profileName, session.EncryptionKey, "session_key", &report)

	sessionPath := filepath.Join(opts.Paths.Data, profileName, "session.enc")
	sessionReady := inspectSessionFile(sessionPath, &report)
	if sessionReady && sessionKeyReady {
		storage := session.KeychainStorage{Profile: profileName, Store: opts.Secrets, Path: sessionPath}
		if plaintext, err := storage.InspectSession(ctx); err != nil {
			report.add("session_decryption", Fail, "session cannot be decrypted", nil)
		} else if len(plaintext) == 0 {
			report.add("session_decryption", Fail, "decrypted session is empty", nil)
		} else {
			report.add("session_decryption", Pass, "session decrypts successfully", nil)
		}
	} else {
		report.add("session_decryption", Skipped, "session file or key is unavailable", nil)
	}

	inspectPeerCache(peerstore.New(opts.Paths.Data, profileName).Path(), &report)
	inspectBinary(&report)

	if !opts.Connect {
		report.add("connectivity", Skipped, "live check not requested", nil)
		report.add("authorization", Skipped, "live check not requested", nil)
	} else if !profileReady || profile.APIID <= 0 || !apiHashReady || opts.Probe == nil {
		report.add("connectivity", Skipped, "live prerequisites are incomplete", nil)
		report.add("authorization", Skipped, "live prerequisites are incomplete", nil)
	} else {
		authorized, err := opts.Probe(ctx)
		if err != nil {
			report.add("connectivity", Fail, "Telegram connection failed", nil)
			report.add("authorization", Skipped, "connectivity check failed", nil)
		} else {
			report.add("connectivity", Pass, "Telegram connection succeeded", nil)
			if authorized {
				report.add("authorization", Pass, "Telegram session is authorized", nil)
			} else {
				report.add("authorization", Fail, "Telegram session is not authorized", nil)
			}
		}
	}

	report.OK = true
	for _, check := range report.Checks {
		if check.Status == Fail {
			report.OK = false
			break
		}
	}
	return report
}

func inspectConfig(path string, report *Report) (config.Config, bool) {
	info, statErr := os.Stat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		report.add("config", Fail, "config file does not exist", nil)
		report.add("config_permissions", Skipped, "config file does not exist", nil)
		return config.Config{}, false
	}
	if statErr != nil {
		report.add("config", Fail, "config file cannot be inspected", nil)
		report.add("config_permissions", Skipped, "config file cannot be inspected", nil)
		return config.Config{}, false
	}
	if info.Mode().Perm()&0o077 != 0 {
		report.add("config_permissions", Fail, "config permissions are insecure", map[string]any{"mode": fmt.Sprintf("%04o", info.Mode().Perm())})
	} else {
		report.add("config_permissions", Pass, "config permissions are private", map[string]any{"mode": fmt.Sprintf("%04o", info.Mode().Perm())})
	}
	b, err := os.ReadFile(path)
	if err != nil {
		report.add("config", Fail, "config file cannot be read", nil)
		return config.Config{}, false
	}
	cfg, err := config.Parse(b)
	if err != nil {
		report.add("config", Fail, "config file is invalid", nil)
		return config.Config{}, false
	}
	report.add("config", Pass, "config file parsed successfully", nil)
	return cfg, true
}

func inspectSecret(ctx context.Context, opts Options, profile, key, name string, report *Report) bool {
	if !opts.SecretBackendSupported || opts.Secrets == nil {
		report.add(name, Skipped, "secret store is unavailable", nil)
		return false
	}
	value, err := opts.Secrets.Get(ctx, profile, key)
	if errors.Is(err, secrets.ErrNotFound) || (err == nil && len(value) == 0) {
		report.add(name, Fail, "secret is missing", nil)
		return false
	}
	if err != nil {
		report.add(name, Fail, "secret could not be read", nil)
		return false
	}
	report.add(name, Pass, "secret is available", nil)
	return true
}

func inspectSessionFile(path string, report *Report) bool {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		report.add("session_file", Fail, "session file does not exist", nil)
		return false
	}
	if err != nil {
		report.add("session_file", Fail, "session file cannot be inspected", nil)
		return false
	}
	if info.Mode().Perm()&0o077 != 0 {
		report.add("session_file", Fail, "session file permissions are insecure", map[string]any{"mode": fmt.Sprintf("%04o", info.Mode().Perm())})
		return true
	}
	report.add("session_file", Pass, "session file permissions are private", map[string]any{"mode": fmt.Sprintf("%04o", info.Mode().Perm())})
	return true
}

func inspectPeerCache(path string, report *Report) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		report.add("peer_cache", Skipped, "peer cache has not been created", nil)
		return
	}
	if err != nil {
		report.add("peer_cache", Fail, "peer cache cannot be inspected", nil)
		return
	}
	if info.Mode().Perm()&0o077 != 0 {
		report.add("peer_cache", Fail, "peer cache permissions are insecure", map[string]any{"mode": fmt.Sprintf("%04o", info.Mode().Perm())})
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		report.add("peer_cache", Fail, "peer cache cannot be read", nil)
		return
	}
	var cache peerstore.Cache
	if err := json.Unmarshal(b, &cache); err != nil {
		report.add("peer_cache", Fail, "peer cache is invalid", nil)
		return
	}
	report.add("peer_cache", Pass, "peer cache parsed successfully", map[string]any{"peers": len(cache.Peers)})
}

func inspectBinary(report *Report) {
	details := map[string]any{"version": report.Version, "commit": report.Commit}
	if report.Executable != "" {
		details["executable"] = report.Executable
	}
	if report.InstalledPath == "" {
		report.add("binary", Warning, "tele was not found on PATH", details)
		return
	}
	details["installed_path"] = report.InstalledPath
	if report.Executable != "" && !samePath(report.Executable, report.InstalledPath) {
		report.add("binary", Warning, "running executable differs from tele on PATH", details)
		return
	}
	report.add("binary", Pass, "running executable matches tele on PATH", details)
}

func (r *Report) add(name string, status Status, message string, details map[string]any) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Message: message, Details: details})
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func samePath(a, b string) bool {
	return cleanPath(a) == cleanPath(b)
}
