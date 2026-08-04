package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	Repository = "ardasevinc/tele"
	Module     = "github.com/ardasevinc/tele/cmd/tele"
	defaultAPI = "https://api.github.com"
	maxBody    = 1 << 20
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

type Status string

const (
	StatusCurrent         Status = "current"
	StatusUpdateAvailable Status = "update_available"
	StatusAhead           Status = "ahead"
	StatusUnknown         Status = "unknown"
)

type Result struct {
	CurrentVersion     string `json:"current_version"`
	CurrentCommit      string `json:"current_commit"`
	LatestVersion      string `json:"latest_version"`
	Status             Status `json:"status"`
	ReleaseURL         string `json:"release_url"`
	ExecutablePath     string `json:"executable_path"`
	ResolvedPath       string `json:"resolved_path"`
	InstallManager     string `json:"install_manager"`
	UpdateSupported    bool   `json:"update_supported"`
	UnsupportedReason  string `json:"unsupported_reason,omitempty"`
	RecommendedCommand string `json:"recommended_command"`
	Applied            bool   `json:"applied"`
	VerifiedVersion    string `json:"verified_version,omitempty"`
}

type Client struct {
	HTTPClient               *http.Client
	APIBase                  string
	Executable               func() (string, error)
	EvalSymlinks             func(string) (string, error)
	GoEnv                    func(context.Context, string) (string, error)
	GoInstall                func(context.Context, string) error
	Preflight                func(context.Context, string, ApplyOptions) (string, error)
	Smoke                    func(context.Context, string) (string, error)
	VerifyCandidate          func(string) error
	RequireOfficialCandidate func() bool
}

type ApplyOptions struct {
	ConfigPath string
	Profile    string
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Immutable  bool   `json:"immutable"`
	Assets     []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

func NewClient() *Client {
	return &Client{}
}

func (c *Client) Check(ctx context.Context, currentVersion, currentCommit string) (Result, error) {
	release, err := c.latest(ctx)
	if err != nil {
		return Result{}, err
	}
	latest := strings.TrimPrefix(release.TagName, "v")
	result := Result{
		CurrentVersion: currentVersion,
		CurrentCommit:  currentCommit,
		LatestVersion:  latest,
		Status:         compare(currentVersion, latest),
		ReleaseURL:     release.HTMLURL,
	}
	target := c.resolveTarget(ctx)
	result.ExecutablePath = target.executable
	result.ResolvedPath = target.resolved
	result.InstallManager = target.manager
	result.UpdateSupported = target.supported
	result.UnsupportedReason = target.reason
	result.RecommendedCommand = recommendedCommand(latest, target.manager)
	if target.manager == "standalone" && !hasStandaloneAssets(release, latest) {
		result.UnsupportedReason = "the immutable release has no matching archive and checksum assets"
		result.RecommendedCommand = "gh release view v" + latest + " --repo " + Repository
	}

	if target.manager == "go-install" && c.requiresOfficialCandidate() && isStableReleaseBuild(currentVersion, currentCommit) {
		result.UpdateSupported = false
		result.UnsupportedReason = "macOS automatic replacement requires an official signed and notarized release; use Homebrew"
		result.RecommendedCommand = "brew install ardasevinc/tap/tele"
	}
	if !isStableReleaseBuild(currentVersion, currentCommit) {
		result.UpdateSupported = false
		result.UnsupportedReason = "development, dirty, or prerelease builds are check-only"
	}
	if !supportedRuntime() {
		result.UpdateSupported = false
		result.UnsupportedReason = "automatic updates are unavailable on this unsupported OS or architecture"
	}
	return result, nil
}

func (c *Client) Apply(ctx context.Context, result Result, options ApplyOptions) (Result, error) {
	if result.Status != StatusUpdateAvailable {
		return result, nil
	}
	if !result.UpdateSupported || result.InstallManager != "go-install" {
		return result, fmt.Errorf("automatic update is unavailable: %s; run %s", result.UnsupportedReason, result.RecommendedCommand)
	}
	tag := "v" + result.LatestVersion
	rollbackPath, err := backupExecutable(result.ResolvedPath)
	if err != nil {
		return result, fmt.Errorf("prepare rollback for %s: %w", tag, err)
	}
	rollback := func(primary error) (Result, error) {
		if restoreErr := restoreExecutable(rollbackPath, result.ResolvedPath); restoreErr != nil {
			return result, errors.Join(primary, fmt.Errorf("restore previous executable: %w", restoreErr))
		}
		return result, primary
	}
	if err := c.goInstall(ctx, Module+"@"+tag); err != nil {
		return rollback(fmt.Errorf("install %s: %w", tag, err))
	}
	if err := c.verifyCandidate(result.ResolvedPath); err != nil {
		return rollback(fmt.Errorf("verify replacement policy for %s: %w", tag, err))
	}
	preflightOutput, err := c.preflight(ctx, result.ResolvedPath, options)
	if err != nil {
		return rollback(fmt.Errorf("compatibility preflight for %s: %w", tag, err))
	}
	if strings.TrimSpace(preflightOutput) != "tele-compatible-v1" {
		return rollback(fmt.Errorf("compatibility preflight reported %q", strings.TrimSpace(preflightOutput)))
	}
	versionOutput, err := c.smoke(ctx, result.ResolvedPath)
	if err != nil {
		return rollback(fmt.Errorf("verify installed %s: %w", tag, err))
	}
	want := fmt.Sprintf("tele version %s (module %s)", result.LatestVersion, tag)
	if strings.TrimSpace(versionOutput) != want {
		return rollback(fmt.Errorf("installed binary reported %q, want %q", strings.TrimSpace(versionOutput), want))
	}
	if err := discardRollback(rollbackPath); err != nil {
		return result, fmt.Errorf("updated and verified %s but could not remove rollback candidate: %w", tag, err)
	}
	result.Applied = true
	result.VerifiedVersion = result.LatestVersion
	return result, nil
}

func (c *Client) latest(ctx context.Context) (githubRelease, error) {
	base := strings.TrimRight(c.APIBase, "/")
	if base == "" {
		base = defaultAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/"+Repository+"/releases/latest", nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tele-updater")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("latest release lookup: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return githubRelease{}, fmt.Errorf("latest release lookup returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return githubRelease{}, fmt.Errorf("read latest release: %w", err)
	}
	if len(body) > maxBody {
		return githubRelease{}, errors.New("latest release metadata exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var release githubRelease
	if err := decoder.Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("decode latest release: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return githubRelease{}, fmt.Errorf("decode latest release: %w", err)
	}
	if release.Draft || release.Prerelease || !release.Immutable {
		return githubRelease{}, errors.New("latest release is not a stable immutable release")
	}
	if !semver.IsValid(release.TagName) || semver.Prerelease(release.TagName) != "" || release.HTMLURL == "" {
		return githubRelease{}, errors.New("latest release metadata is invalid")
	}
	return release, nil
}

type installTarget struct {
	executable string
	resolved   string
	manager    string
	supported  bool
	reason     string
}

func (c *Client) resolveTarget(ctx context.Context) installTarget {
	executable, err := c.executable()
	if err != nil || !filepath.IsAbs(executable) {
		return installTarget{executable: executable, resolved: executable, manager: "unknown", reason: "running executable path is unavailable or not absolute"}
	}
	resolved, err := c.evalSymlinks(executable)
	if err != nil || !filepath.IsAbs(resolved) {
		return installTarget{executable: executable, resolved: resolved, manager: "unknown", reason: "running executable symlinks cannot be resolved unambiguously"}
	}
	target := installTarget{executable: filepath.Clean(executable), resolved: filepath.Clean(resolved)}
	if isHomebrewPath(target.resolved) {
		target.manager = "homebrew"
		target.reason = "Homebrew owns this installation"
		return target
	}
	goTarget, err := c.goInstallTarget(ctx)
	if err == nil && samePath(target.resolved, goTarget) {
		target.manager = "go-install"
		target.supported = pathWritable(goTarget)
		if !target.supported {
			target.reason = "Go install destination is not writable"
		}
		return target
	}
	target.manager = "standalone"
	target.reason = "standalone replacement requires native GitHub attestation verification"
	return target
}

func (c *Client) goInstallTarget(ctx context.Context) (string, error) {
	gobin, err := c.goEnv(ctx, "GOBIN")
	if err != nil {
		return "", err
	}
	if gobin == "" {
		gopath, err := c.goEnv(ctx, "GOPATH")
		if err != nil {
			return "", err
		}
		first := strings.Split(gopath, string(os.PathListSeparator))[0]
		if first == "" {
			return "", errors.New("go env GOPATH is empty")
		}
		gobin = filepath.Join(first, "bin")
	}
	return filepath.Clean(filepath.Join(gobin, executableName())), nil
}

func compare(current, latest string) Status {
	currentTag, latestTag := "v"+strings.TrimPrefix(current, "v"), "v"+strings.TrimPrefix(latest, "v")
	if !semver.IsValid(currentTag) || !semver.IsValid(latestTag) {
		return StatusUnknown
	}
	switch semver.Compare(currentTag, latestTag) {
	case -1:
		return StatusUpdateAvailable
	case 1:
		return StatusAhead
	default:
		return StatusCurrent
	}
}

func isStableReleaseBuild(version, commit string) bool {
	tag := "v" + strings.TrimPrefix(version, "v")
	provenanceValid := commitPattern.MatchString(commit) || commit == "module "+tag
	return semver.IsValid(tag) && semver.Prerelease(tag) == "" && provenanceValid
}

func recommendedCommand(version, manager string) string {
	tag := "v" + strings.TrimPrefix(version, "v")
	switch manager {
	case "homebrew":
		return "brew upgrade tele"
	case "go-install":
		return "go install " + Module + "@" + tag
	default:
		asset := fmt.Sprintf("tele_%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), runtime.GOOS, normalizedArch(runtime.GOARCH))
		checksum := "sha256sum --check"
		if runtime.GOOS == "darwin" {
			checksum = "shasum -a 256 --check"
		}
		return fmt.Sprintf("gh release download %s --repo %s --pattern checksums.txt --pattern '%s' && gh attestation verify checksums.txt --repo %s && gh attestation verify '%s' --repo %s && grep -F '  %s' checksums.txt > '%s.sha256' && %s '%s.sha256'", tag, Repository, asset, Repository, asset, Repository, asset, asset, checksum, asset)
	}
}

func hasStandaloneAssets(release githubRelease, version string) bool {
	asset := fmt.Sprintf("tele_%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), runtime.GOOS, normalizedArch(runtime.GOARCH))
	foundArchive, foundChecksums := false, false
	for _, candidate := range release.Assets {
		switch candidate.Name {
		case asset:
			foundArchive = true
		case "checksums.txt":
			foundChecksums = true
		}
	}
	return foundArchive && foundChecksums
}

func normalizedArch(arch string) string {
	if arch == "aarch64" {
		return "arm64"
	}
	if arch == "x86_64" {
		return "amd64"
	}
	return arch
}

func supportedRuntime() bool {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	arch := normalizedArch(runtime.GOARCH)
	return arch == "amd64" || arch == "arm64"
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "tele.exe"
	}
	return "tele"
}

func isHomebrewPath(path string) bool {
	path = filepath.ToSlash(path)
	return strings.Contains(path, "/Cellar/tele/") || strings.Contains(path, "/Cellar/tele@")
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func pathWritable(path string) bool {
	info, err := os.Stat(path)
	if err == nil && !info.Mode().IsRegular() {
		return false
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	return directoryWritable(filepath.Dir(path))
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}

func (c *Client) executable() (string, error) {
	if c.Executable != nil {
		return c.Executable()
	}
	return os.Executable()
}

func (c *Client) evalSymlinks(path string) (string, error) {
	if c.EvalSymlinks != nil {
		return c.EvalSymlinks(path)
	}
	return filepath.EvalSymlinks(path)
}

func (c *Client) goEnv(ctx context.Context, key string) (string, error) {
	if key != "GOBIN" && key != "GOPATH" {
		return "", fmt.Errorf("unsupported go env key %q", key)
	}
	if c.GoEnv != nil {
		return c.GoEnv(ctx, key)
	}
	output, err := exec.CommandContext(ctx, "go", "env", key).Output() // #nosec G204 -- key is restricted to GOBIN or GOPATH above.
	return strings.TrimSpace(string(output)), err
}

func (c *Client) goInstall(ctx context.Context, module string) error {
	if c.GoInstall != nil {
		return c.GoInstall(ctx, module)
	}
	cmd := exec.CommandContext(ctx, "go", "install", module) // #nosec G204 -- module is fixed and the tag came from validated immutable release metadata.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func (c *Client) smoke(ctx context.Context, path string) (string, error) {
	if c.Smoke != nil {
		return c.Smoke(ctx, path)
	}
	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput() // #nosec G204 -- path is the resolved running executable and Go install destination.
	return string(output), err
}

func (c *Client) preflight(ctx context.Context, path string, options ApplyOptions) (string, error) {
	if c.Preflight != nil {
		return c.Preflight(ctx, path, options)
	}
	args := make([]string, 0, 6)
	if options.ConfigPath != "" {
		args = append(args, "--config", options.ConfigPath)
	}
	if options.Profile != "" {
		args = append(args, "--profile", options.Profile)
	}
	args = append(args, "internal", "compatibility")
	output, err := exec.CommandContext(ctx, path, args...).CombinedOutput() // #nosec G204 -- path is the resolved Go install destination and arguments are fixed or locally selected paths/profile names.
	return string(output), err
}

func (c *Client) verifyCandidate(path string) error {
	if c.VerifyCandidate != nil {
		return c.VerifyCandidate(path)
	}
	return verifyCandidate(path)
}

func (c *Client) requiresOfficialCandidate() bool {
	if c.RequireOfficialCandidate != nil {
		return c.RequireOfficialCandidate()
	}
	return requiresOfficialCandidate()
}

func backupExecutable(path string) (string, error) {
	source, err := os.Open(path) // #nosec G304 -- path is the resolved running executable and exact Go install target.
	if err != nil {
		return "", err
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("running executable is not a regular file")
		}
		return "", err
	}
	target, err := os.CreateTemp(filepath.Dir(path), ".tele-rollback-*")
	if err != nil {
		return "", err
	}
	rollbackPath := target.Name()
	complete := false
	defer func() {
		if !complete {
			_ = target.Close()
			_ = os.Remove(rollbackPath)
		}
	}()
	if err := target.Chmod(info.Mode().Perm()); err != nil {
		return "", err
	}
	if _, err := io.Copy(target, source); err != nil {
		return "", err
	}
	if err := target.Sync(); err != nil {
		return "", err
	}
	if err := target.Close(); err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return "", err
	}
	complete = true
	return rollbackPath, nil
}

func restoreExecutable(rollbackPath, targetPath string) error {
	if err := os.Rename(rollbackPath, targetPath); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(targetPath))
}

func discardRollback(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	dir, err := os.Open(path) // #nosec G304 -- path is the resolved executable's directory.
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
