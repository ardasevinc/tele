package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/peerstore"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/session"
)

type memoryStore struct {
	values map[string][]byte
}

func (s *memoryStore) Get(_ context.Context, profile, key string) ([]byte, error) {
	value, ok := s.values[profile+":"+key]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *memoryStore) Set(_ context.Context, profile, key string, value []byte) error {
	s.values[profile+":"+key] = append([]byte(nil), value...)
	return nil
}

func (s *memoryStore) Delete(_ context.Context, profile, key string) error {
	delete(s.values, profile+":"+key)
	return nil
}

type fixture struct {
	opts        Options
	store       *memoryStore
	sessionPath string
	peerPath    string
}

func validFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{
		Config: filepath.Join(root, "config", "config.toml"),
		Data:   filepath.Join(root, "data"),
	}
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	configBody := []byte("default_profile = \"main\"\n[profiles.main]\napi_id = 12345\n")
	if err := os.WriteFile(paths.Config, configBody, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{values: map[string][]byte{"main:api-hash": []byte("SUPERSECRET")}}
	sessionPath := filepath.Join(paths.Data, "main", "session.enc")
	storage := session.KeychainStorage{Profile: "main", Store: store, Path: sessionPath}
	if err := storage.StoreSession(context.Background(), []byte("valid gotd session bytes")); err != nil {
		t.Fatal(err)
	}
	peerStore := peerstore.New(paths.Data, "main")
	if err := peerStore.Save(peerstore.Cache{Peers: []peerstore.Peer{{Ref: "user:1", Kind: "user", ID: 1, AccessHash: 2, Title: "Alice"}}}); err != nil {
		t.Fatal(err)
	}
	return fixture{
		opts: Options{
			Paths:                  paths,
			Secrets:                store,
			SecretBackend:          "test keychain",
			SecretBackendSupported: true,
			Version:                "test",
			Commit:                 "abc123",
			Executable:             filepath.Join(root, "bin", "tele"),
			InstalledPath:          filepath.Join(root, "bin", "tele"),
		},
		store:       store,
		sessionPath: sessionPath,
		peerPath:    peerStore.Path(),
	}
}

func TestRunReportsReadyLocalStateWithoutLiveAccess(t *testing.T) {
	fx := validFixture(t)
	report := Run(context.Background(), fx.opts)
	if !report.OK {
		t.Fatalf("report not ready: %+v", report.Checks)
	}
	for _, name := range []string{"config", "config_permissions", "profile", "api_id", "secret_store", "api_hash", "session_key", "session_file", "session_decryption", "peer_cache", "binary"} {
		if got := checkNamed(t, report, name).Status; got != Pass {
			t.Errorf("%s = %s, want pass", name, got)
		}
	}
	if checkNamed(t, report, "connectivity").Status != Skipped || checkNamed(t, report, "authorization").Status != Skipped {
		t.Fatal("local doctor did not skip live checks")
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SUPERSECRET") || strings.Contains(string(b), "valid gotd session bytes") {
		t.Fatalf("doctor leaked secret material: %s", b)
	}
}

func TestRunAggregatesPartialConfigAndUnsupportedPlatform(t *testing.T) {
	fx := validFixture(t)
	if err := os.Remove(fx.opts.Paths.Config); err != nil {
		t.Fatal(err)
	}
	fx.opts.SecretBackend = "unsupported on linux"
	fx.opts.SecretBackendSupported = false
	report := Run(context.Background(), fx.opts)
	if report.OK {
		t.Fatal("partial unsupported setup reported ready")
	}
	if checkNamed(t, report, "config").Status != Fail {
		t.Fatal("missing config was not failed")
	}
	if checkNamed(t, report, "profile").Status != Skipped {
		t.Fatal("dependent profile check was not skipped")
	}
	if checkNamed(t, report, "secret_store").Status != Fail {
		t.Fatal("unsupported secret store was not failed")
	}
	if len(report.Checks) < 10 {
		t.Fatalf("doctor aborted early with %d checks", len(report.Checks))
	}
}

func TestRunDiagnosesPartialConfigWithoutAborting(t *testing.T) {
	fx := validFixture(t)
	if err := os.WriteFile(fx.opts.Paths.Config, []byte("default_profile = \"main\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), fx.opts)
	if checkNamed(t, report, "config").Status != Pass || checkNamed(t, report, "profile").Status != Pass {
		t.Fatalf("partial config was not parsed: %+v", report.Checks)
	}
	if checkNamed(t, report, "api_id").Status != Fail {
		t.Fatal("missing API ID was not diagnosed")
	}
	if len(report.Checks) < 10 {
		t.Fatalf("doctor aborted early with %d checks", len(report.Checks))
	}
}

func TestRunDetectsMissingKeyAndCorruptSessionAndCache(t *testing.T) {
	t.Run("missing key", func(t *testing.T) {
		fx := validFixture(t)
		delete(fx.store.values, "main:"+session.EncryptionKey)
		report := Run(context.Background(), fx.opts)
		if checkNamed(t, report, "session_key").Status != Fail || checkNamed(t, report, "session_decryption").Status != Skipped {
			t.Fatalf("missing-key checks = %+v", report.Checks)
		}
	})
	t.Run("corrupt state", func(t *testing.T) {
		fx := validFixture(t)
		if err := os.WriteFile(fx.sessionPath, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fx.peerPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		report := Run(context.Background(), fx.opts)
		if checkNamed(t, report, "session_decryption").Status != Fail {
			t.Fatal("corrupt session was not failed")
		}
		if checkNamed(t, report, "peer_cache").Status != Fail {
			t.Fatal("corrupt peer cache was not failed")
		}
	})
}

func TestRunDetectsInsecureModesWithoutRepairingThem(t *testing.T) {
	fx := validFixture(t)
	for _, path := range []string{fx.opts.Paths.Config, fx.sessionPath, fx.peerPath} {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := Run(context.Background(), fx.opts)
	for _, name := range []string{"config_permissions", "session_file", "peer_cache"} {
		if checkNamed(t, report, name).Status != Fail {
			t.Errorf("%s did not fail", name)
		}
	}
	for _, path := range []string{fx.opts.Paths.Config, fx.sessionPath, fx.peerPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("doctor mutated %s mode to %04o", path, info.Mode().Perm())
		}
	}
}

func TestRunPerformsOptInConnectivityAndAuthChecks(t *testing.T) {
	fx := validFixture(t)
	fx.opts.Connect = true
	fx.opts.Probe = func(context.Context) (bool, error) { return true, nil }
	report := Run(context.Background(), fx.opts)
	if checkNamed(t, report, "connectivity").Status != Pass || checkNamed(t, report, "authorization").Status != Pass {
		t.Fatalf("live checks = %+v", report.Checks)
	}

	fx.opts.Probe = func(context.Context) (bool, error) { return false, nil }
	report = Run(context.Background(), fx.opts)
	if checkNamed(t, report, "connectivity").Status != Pass || checkNamed(t, report, "authorization").Status != Fail {
		t.Fatalf("unauthorized checks = %+v", report.Checks)
	}

	fx.opts.Probe = func(context.Context) (bool, error) { return false, errors.New("dial failed with SUPERSECRET") }
	report = Run(context.Background(), fx.opts)
	if checkNamed(t, report, "connectivity").Status != Fail || checkNamed(t, report, "authorization").Status != Skipped {
		t.Fatalf("failed live checks = %+v", report.Checks)
	}
	b, _ := json.Marshal(report)
	if strings.Contains(string(b), "dial failed") || strings.Contains(string(b), "SUPERSECRET") {
		t.Fatalf("doctor exposed probe error: %s", b)
	}
}

func TestRunWarnsAboutInstalledPathDrift(t *testing.T) {
	fx := validFixture(t)
	fx.opts.InstalledPath = filepath.Join(t.TempDir(), "tele")
	check := checkNamed(t, Run(context.Background(), fx.opts), "binary")
	if check.Status != Warning {
		t.Fatalf("binary status = %s, want warning", check.Status)
	}
}

func checkNamed(t *testing.T, report Report, name string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing check %q in %+v", name, report.Checks)
	return Check{}
}
