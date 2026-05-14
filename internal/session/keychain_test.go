package session

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ardasevinc/tele/internal/secrets"
	gotdsession "github.com/gotd/td/session"
)

type missingStore struct{}

func (missingStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, secrets.ErrNotFound
}

func (missingStore) Set(context.Context, string, string, []byte) error {
	return nil
}

func (missingStore) Delete(context.Context, string, string) error {
	return nil
}

func TestLoadSessionMapsMissingSecretToGotdErrNotFound(t *testing.T) {
	storage := KeychainStorage{Profile: "test", Store: missingStore{}, Path: t.TempDir() + "/missing.enc"}
	_, err := storage.LoadSession(context.Background())
	if !errors.Is(err, gotdsession.ErrNotFound) {
		t.Fatalf("err = %v, want %v", err, gotdsession.ErrNotFound)
	}
}

type memoryStore struct {
	values map[string][]byte
}

func (m memoryStore) Get(_ context.Context, _ string, key string) ([]byte, error) {
	v, ok := m.values[key]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return v, nil
}

func (m memoryStore) Set(_ context.Context, _ string, key string, value []byte) error {
	m.values[key] = value
	return nil
}

func (m memoryStore) Delete(_ context.Context, _ string, key string) error {
	delete(m.values, key)
	return nil
}

func TestSessionRoundTripUsesEncryptedFileAndKeyStore(t *testing.T) {
	store := memoryStore{values: map[string][]byte{}}
	path := t.TempDir() + "/session.enc"
	storage := KeychainStorage{Profile: "test", Store: store, Path: path}
	want := []byte("binary\x00session\xffdata")
	if err := storage.StoreSession(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := storage.LoadSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("session = %q, want %q", got, want)
	}
	if string(got) == string(mustRead(t, path)) {
		t.Fatal("session file stored plaintext")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
