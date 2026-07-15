package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ardasevinc/tele/internal/secrets"
	gotdsession "github.com/gotd/td/session"
)

type missingStore struct{}

func (missingStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, secrets.ErrNotFound
}

func TestConcurrentFirstSessionWritesKeepKeyAndCiphertextConsistent(t *testing.T) {
	store := memoryStore{values: map[string][]byte{}}
	storage := KeychainStorage{Profile: "test", Store: store, Path: filepath.Join(t.TempDir(), "session.enc")}
	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- storage.StoreSession(context.Background(), []byte(fmt.Sprintf("session-%02d", i)))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := storage.LoadSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len("session-00") {
		t.Fatalf("session = %q", got)
	}
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

func TestLoadSessionRepairsExistingModes(t *testing.T) {
	store := memoryStore{values: map[string][]byte{}}
	dir := filepath.Join(t.TempDir(), "test")
	path := filepath.Join(dir, "session.enc")
	storage := KeychainStorage{Profile: "test", Store: store, Path: path}
	if err := storage.StoreSession(context.Background(), []byte("session")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.LoadSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", target, got, want)
		}
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
