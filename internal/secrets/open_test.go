package secrets

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOpenRequiresExplicitBackendOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS intentionally preserves the legacy Keychain default")
	}
	if _, err := Open(context.Background(), Selection{}, OpenOptions{Profile: "main"}); !errors.Is(err, ErrBackendUnconfigured) {
		t.Fatalf("Open error = %v, want ErrBackendUnconfigured", err)
	}
}

func TestOpenRejectsUnknownBackend(t *testing.T) {
	if _, err := Open(context.Background(), Selection{Backend: "future-v9"}, OpenOptions{Profile: "main"}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Open error = %v, want ErrBackendUnavailable", err)
	}
}

func TestLazyStoreOpensBackendOnce(t *testing.T) {
	var calls atomic.Int32
	backend := &memorySecretStore{values: map[string][]byte{"main:key": []byte("value")}}
	lazy := &LazyStore{Open: func(context.Context) (Store, error) {
		calls.Add(1)
		return backend, nil
	}}
	const readers = 20
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := lazy.Get(context.Background(), "main", "key"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("backend open calls = %d", calls.Load())
	}
}

func TestLazyStoreForwardsCatalogDiagnostics(t *testing.T) {
	want := CatalogDiagnostics{Generation: 7, Mappings: 4, PhysicalItems: 5, Orphans: 1}
	backend := &catalogMemorySecretStore{
		memorySecretStore: memorySecretStore{values: map[string][]byte{}},
		diagnostics:       want,
	}
	lazy := &LazyStore{Open: func(context.Context) (Store, error) {
		return backend, nil
	}}

	got, err := lazy.CatalogDiagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("CatalogDiagnostics = %+v, want %+v", got, want)
	}
}

type catalogMemorySecretStore struct {
	memorySecretStore
	diagnostics CatalogDiagnostics
}

func (s *catalogMemorySecretStore) CatalogDiagnostics(context.Context) (CatalogDiagnostics, error) {
	return s.diagnostics, nil
}

type memorySecretStore struct {
	values map[string][]byte
}

func (s *memorySecretStore) Get(_ context.Context, profile, key string) ([]byte, error) {
	value, ok := s.values[profile+":"+key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *memorySecretStore) Set(_ context.Context, profile, key string, value []byte) error {
	s.values[profile+":"+key] = append([]byte(nil), value...)
	return nil
}

func (s *memorySecretStore) Delete(_ context.Context, profile, key string) error {
	delete(s.values, profile+":"+key)
	return nil
}
