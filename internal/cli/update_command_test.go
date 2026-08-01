package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/buildinfo"
	"github.com/ardasevinc/tele/internal/updater"
)

func TestUpdateCheckAndExplicitApply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/ardasevinc/tele/releases/latest" {
			t.Fatalf("release path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","html_url":"https://example.test/v1.2.0","immutable":true}`))
	}))
	defer server.Close()
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "tele")
	if err := os.WriteFile(bin, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldCommit := buildinfo.Version, buildinfo.Commit
	buildinfo.Version, buildinfo.Commit = "1.1.0", "abcdef0123456789"
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = oldVersion, oldCommit })

	installed := ""
	client := &updater.Client{
		HTTPClient: server.Client(), APIBase: server.URL,
		Executable:   func() (string, error) { return bin, nil },
		EvalSymlinks: func(path string) (string, error) { return path, nil },
		GoEnv: func(context.Context, string) (string, error) {
			return binDir, nil
		},
		GoInstall: func(_ context.Context, module string) error {
			installed = module
			return nil
		},
		Preflight: func(context.Context, string, updater.ApplyOptions) (string, error) {
			return "tele-compatible-v1", nil
		},
		Smoke: func(context.Context, string) (string, error) {
			return "tele version 1.2.0 (module v1.2.0)\n", nil
		},
	}

	var stdout, stderr bytes.Buffer
	state := &appState{in: strings.NewReader(""), out: &stdout, err: &stderr, updateClient: client}
	if err := executeWithState(context.Background(), []string{"--json", "update", "--check"}, state); err != nil {
		t.Fatalf("update --check: %v stderr=%s", err, stderr.String())
	}
	var envelope struct {
		Data updater.Result `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Status != updater.StatusUpdateAvailable || envelope.Data.Applied || !envelope.Data.UpdateSupported {
		t.Fatalf("check result = %+v", envelope.Data)
	}

	stdout.Reset()
	stderr.Reset()
	state = &appState{in: strings.NewReader(""), out: &stdout, err: &stderr, updateClient: client}
	if err := executeWithState(context.Background(), []string{"--json", "update", "--yes"}, state); err != nil {
		t.Fatalf("update --yes: %v stderr=%s", err, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if installed != updater.Module+"@v1.2.0" || !envelope.Data.Applied || envelope.Data.VerifiedVersion != "1.2.0" {
		t.Fatalf("installed=%q apply result=%+v", installed, envelope.Data)
	}
}

func TestUpdateRequiresOneExplicitMode(t *testing.T) {
	for _, args := range [][]string{{"update"}, {"update", "--check", "--yes"}} {
		state := &appState{in: strings.NewReader(""), out: &bytes.Buffer{}, err: &bytes.Buffer{}}
		err := executeWithState(context.Background(), args, state)
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}
