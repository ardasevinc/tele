package updater

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckUsesImmutableStableReleaseAndExactGoTarget(t *testing.T) {
	server := releaseServer(t, releasePayload("v1.2.0"))
	defer server.Close()
	bin := filepath.Join(t.TempDir(), executableName())
	if err := os.WriteFile(bin, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &Client{
		HTTPClient: server.Client(), APIBase: server.URL,
		Executable:   func() (string, error) { return bin, nil },
		EvalSymlinks: func(path string) (string, error) { return path, nil },
		GoEnv: func(_ context.Context, key string) (string, error) {
			if key == "GOBIN" {
				return filepath.Dir(bin), nil
			}
			return "", fmt.Errorf("unexpected go env %s", key)
		},
	}
	result, err := client.Check(context.Background(), "1.1.0", "abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUpdateAvailable || result.InstallManager != "go-install" || !result.UpdateSupported {
		t.Fatalf("result = %+v", result)
	}
	if result.RecommendedCommand != "go install "+Module+"@v1.2.0" || result.ResolvedPath != bin || result.Applied {
		t.Fatalf("result = %+v", result)
	}
}

func TestCheckClassifiesHomebrewStandaloneAndUnsafeBuilds(t *testing.T) {
	server := releaseServer(t, releasePayload("v1.2.0"))
	defer server.Close()
	tests := []struct {
		name, executable, version, commit, manager, command, reason string
	}{
		{"homebrew", "/opt/homebrew/Cellar/tele/1.1.0/bin/tele", "1.1.0", "abcdef0", "homebrew", "brew upgrade tele", "Homebrew"},
		{"standalone", "/opt/tele/bin/tele", "1.1.0", "abcdef0", "standalone", "gh release download", "attestation"},
		{"dirty", "/go/bin/tele", "1.1.0", "abcdef0-dirty", "go-install", "go install", "check-only"},
		{"prerelease", "/go/bin/tele", "1.2.0-rc.1", "abcdef0", "go-install", "go install", "check-only"},
		{"unknown provenance", "/go/bin/tele", "1.1.0", "locally-built", "go-install", "go install", "check-only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				HTTPClient: server.Client(), APIBase: server.URL,
				Executable:   func() (string, error) { return tt.executable, nil },
				EvalSymlinks: func(path string) (string, error) { return path, nil },
				GoEnv: func(_ context.Context, key string) (string, error) {
					if key == "GOBIN" {
						return "/go/bin", nil
					}
					return "", nil
				},
			}
			result, err := client.Check(context.Background(), tt.version, tt.commit)
			if err != nil {
				t.Fatal(err)
			}
			if result.InstallManager != tt.manager || result.UpdateSupported || !strings.Contains(result.RecommendedCommand, tt.command) || !strings.Contains(result.UnsupportedReason, tt.reason) {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestApplyPinsAndVerifiesGoInstall(t *testing.T) {
	var installed string
	bin := filepath.Join(t.TempDir(), executableName())
	if err := os.WriteFile(bin, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &Client{
		GoInstall: func(_ context.Context, module string) error {
			installed = module
			return os.WriteFile(bin, []byte("new executable"), 0o755)
		},
		Preflight: func(_ context.Context, path string, options ApplyOptions) (string, error) {
			if path != bin || options.ConfigPath != "/config" || options.Profile != "main" {
				t.Fatalf("preflight path=%q options=%+v", path, options)
			}
			return "tele-compatible-v1\n", nil
		},
		Smoke: func(_ context.Context, path string) (string, error) {
			if path != bin {
				t.Fatalf("smoke path = %q", path)
			}
			return "tele version 1.2.0 (module v1.2.0)\n", nil
		},
	}
	result, err := client.Apply(context.Background(), Result{
		LatestVersion: "1.2.0", Status: StatusUpdateAvailable, InstallManager: "go-install",
		UpdateSupported: true, ResolvedPath: bin,
	}, ApplyOptions{ConfigPath: "/config", Profile: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if installed != Module+"@v1.2.0" || !result.Applied || result.VerifiedVersion != "1.2.0" {
		t.Fatalf("installed=%q result=%+v", installed, result)
	}
	if body, err := os.ReadFile(bin); err != nil || string(body) != "new executable" {
		t.Fatalf("installed executable = %q, %v", body, err)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(bin), ".tele-rollback-*")); err != nil || len(matches) != 0 {
		t.Fatalf("rollback candidates = %v, %v", matches, err)
	}
}

func TestApplyRefusesUnsupportedMutation(t *testing.T) {
	_, err := (&Client{}).Apply(context.Background(), Result{
		LatestVersion: "1.2.0", Status: StatusUpdateAvailable, InstallManager: "standalone",
		UnsupportedReason: "attestation required", RecommendedCommand: "verify manually",
	}, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "verify manually") {
		t.Fatalf("Apply error = %v", err)
	}
}

func TestApplyRestoresExecutableOnPreflightOrSmokeFailure(t *testing.T) {
	for _, failure := range []string{"preflight", "smoke"} {
		t.Run(failure, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), executableName())
			if err := os.WriteFile(bin, []byte("old executable"), 0o755); err != nil {
				t.Fatal(err)
			}
			client := &Client{
				GoInstall: func(context.Context, string) error {
					return os.WriteFile(bin, []byte("new executable"), 0o755)
				},
				Preflight: func(context.Context, string, ApplyOptions) (string, error) {
					if failure == "preflight" {
						return "", errors.New("incompatible data")
					}
					return "tele-compatible-v1", nil
				},
				Smoke: func(context.Context, string) (string, error) {
					if failure == "smoke" {
						return "tele version wrong", nil
					}
					return "tele version 1.2.0 (module v1.2.0)", nil
				},
			}
			_, err := client.Apply(context.Background(), Result{
				LatestVersion: "1.2.0", Status: StatusUpdateAvailable, InstallManager: "go-install",
				UpdateSupported: true, ResolvedPath: bin,
			}, ApplyOptions{})
			if err == nil {
				t.Fatal("Apply accepted failed candidate verification")
			}
			body, readErr := os.ReadFile(bin)
			if readErr != nil || string(body) != "old executable" {
				t.Fatalf("restored executable = %q, %v; Apply error = %v", body, readErr, err)
			}
		})
	}
}

func TestLatestRejectsMutableOrPrereleaseMetadata(t *testing.T) {
	for _, payload := range []string{
		`{"tag_name":"v1.2.0","html_url":"https://example.test","immutable":false}`,
		`{"tag_name":"v1.2.0-rc.1","html_url":"https://example.test","immutable":true,"prerelease":true}`,
	} {
		server := releaseServer(t, payload)
		client := &Client{HTTPClient: server.Client(), APIBase: server.URL}
		if _, err := client.latest(context.Background()); err == nil {
			t.Fatalf("latest accepted %s", payload)
		}
		server.Close()
	}
}

func TestLatestRejectsBadHTTPAndMalformedMetadata(t *testing.T) {
	oversized := strings.Repeat("x", maxBody+1)
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "http status", statusCode: http.StatusServiceUnavailable, body: `{"message":"try later"}`},
		{name: "malformed JSON", statusCode: http.StatusOK, body: `{"tag_name":`},
		{name: "trailing JSON", statusCode: http.StatusOK, body: releasePayload("v1.2.0") + `{}`},
		{name: "oversized response", statusCode: http.StatusOK, body: oversized},
		{name: "invalid tag", statusCode: http.StatusOK, body: `{"tag_name":"latest","html_url":"https://example.test","immutable":true}`},
		{name: "missing URL", statusCode: http.StatusOK, body: `{"tag_name":"v1.2.0","immutable":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client := &Client{HTTPClient: server.Client(), APIBase: server.URL}
			if _, err := client.latest(context.Background()); err == nil {
				t.Fatal("latest accepted hostile release metadata")
			}
		})
	}
}

func TestCheckRejectsIncompleteStandaloneAssets(t *testing.T) {
	payload := `{"tag_name":"v1.2.0","html_url":"https://example.test/v1.2.0","immutable":true,"assets":[{"name":"checksums.txt"}]}`
	server := releaseServer(t, payload)
	defer server.Close()
	client := &Client{
		HTTPClient: server.Client(), APIBase: server.URL,
		Executable:   func() (string, error) { return "/opt/tele/bin/tele", nil },
		EvalSymlinks: func(path string) (string, error) { return path, nil },
		GoEnv:        func(context.Context, string) (string, error) { return "/go/bin", nil },
	}
	result, err := client.Check(context.Background(), "1.1.0", "abcdef0")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateSupported || !strings.Contains(result.UnsupportedReason, "no matching archive") || result.RecommendedCommand != "gh release view v1.2.0 --repo "+Repository {
		t.Fatalf("result = %+v", result)
	}
}

func TestCheckReportsUnavailableExecutableWithoutInventingAPath(t *testing.T) {
	server := releaseServer(t, releasePayload("v1.2.0"))
	defer server.Close()
	client := &Client{
		HTTPClient: server.Client(), APIBase: server.URL,
		Executable: func() (string, error) { return "", errors.New("unavailable") },
	}
	result, err := client.Check(context.Background(), "1.1.0", "abcdef0")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExecutablePath != "" || result.ResolvedPath != "" || result.InstallManager != "unknown" || result.UpdateSupported {
		t.Fatalf("result = %+v", result)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tt := range []struct {
		current, latest string
		want            Status
	}{
		{"1.1.0", "1.2.0", StatusUpdateAvailable},
		{"1.2.0", "1.2.0", StatusCurrent},
		{"1.3.0", "1.2.0", StatusAhead},
		{"dev", "1.2.0", StatusUnknown},
	} {
		if got := compare(tt.current, tt.latest); got != tt.want {
			t.Fatalf("compare(%q, %q) = %q, want %q", tt.current, tt.latest, got, tt.want)
		}
	}
}

func releaseServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+Repository+"/releases/latest" || r.Header.Get("User-Agent") != "tele-updater" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Fatalf("request = %s headers=%v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
}

func releasePayload(tag string) string {
	version := strings.TrimPrefix(tag, "v")
	asset := fmt.Sprintf("tele_%s_%s_%s.tar.gz", version, runtime.GOOS, normalizedArch(runtime.GOARCH))
	return fmt.Sprintf(`{"tag_name":%q,"html_url":"https://example.test/%s","immutable":true,"assets":[{"name":"checksums.txt"},{"name":%q}]}`, tag, tag, asset)
}
