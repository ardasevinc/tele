package botapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetMe(t *testing.T) {
	const token = "123:secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+token+"/getMe" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":123,"is_bot":true,"username":"ManagerBot","can_manage_bots":true}}`))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.BaseURL = server.URL
	got, err := client.GetMe(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 123 || got.Username != "ManagerBot" || !got.IsBot || !got.CanManageBots {
		t.Fatalf("bot = %+v", got)
	}
}

func TestGetMeNeverLeaksTokenInErrors(t *testing.T) {
	const token = "123:super-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"bad token 123:super-secret"}`))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.BaseURL = server.URL
	_, err := client.GetMe(context.Background(), token)
	if err == nil {
		t.Fatal("GetMe succeeded")
	}
	if strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("unsafe error = %q", err)
	}
}

func TestGetMeRejectsInvalidResponses(t *testing.T) {
	for _, body := range []string{`not-json`, `{"ok":true}`, `{"ok":true,"result":"wrong"}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			client := NewClient(server.Client())
			client.BaseURL = server.URL
			if _, err := client.GetMe(context.Background(), "123:test"); err == nil {
				t.Fatal("GetMe accepted invalid response")
			}
		})
	}
}
