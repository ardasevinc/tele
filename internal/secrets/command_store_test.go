package secrets

import (
	"context"
	"errors"
	"testing"
)

func TestCommandStoreReusesSuccessfulReadsAndReturnsCopies(t *testing.T) {
	backend := &commandStoreBackend{values: map[string][]byte{"main:key": []byte("secret")}}
	store := NewCommandStore(backend)

	first, err := store.Get(context.Background(), "main", "key")
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 'X'
	second, err := store.Get(context.Background(), "main", "key")
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != "secret" || backend.gets != 1 {
		t.Fatalf("second read = %q, backend gets = %d", second, backend.gets)
	}
}

func TestCommandStoreInvalidatesAfterMutation(t *testing.T) {
	backend := &commandStoreBackend{values: map[string][]byte{"main:key": []byte("old")}}
	store := NewCommandStore(backend)
	if _, err := store.Get(context.Background(), "main", "key"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "main", "key", []byte("new")); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), "main", "key")
	if err != nil || string(value) != "new" || backend.gets != 2 {
		t.Fatalf("read after Set = %q, %v; backend gets = %d", value, err, backend.gets)
	}
	if err := store.Delete(context.Background(), "main", "key"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "main", "key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read after Delete = %v", err)
	}
}

func TestCommandStoreRetriesOrdinaryFailuresButStopsAfterAccessFailure(t *testing.T) {
	backend := &commandStoreBackend{values: map[string][]byte{}}
	store := NewCommandStore(backend)
	for range 2 {
		if _, err := store.Get(context.Background(), "main", "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing read = %v", err)
		}
	}
	if backend.gets != 2 {
		t.Fatalf("ordinary failed reads = %d, want 2", backend.gets)
	}

	backend.readErr = &BackendError{Kind: ErrInteractionRequired, Backend: BackendKeychain}
	if _, err := store.Get(context.Background(), "main", "blocked"); !errors.Is(err, ErrInteractionRequired) {
		t.Fatalf("first blocked read = %v", err)
	}
	if _, err := store.Get(context.Background(), "main", "later"); !errors.Is(err, ErrInteractionRequired) {
		t.Fatalf("later blocked read = %v", err)
	}
	if _, err := store.CatalogDiagnostics(context.Background()); !errors.Is(err, ErrInteractionRequired) {
		t.Fatalf("blocked diagnostics = %v", err)
	}
	if backend.gets != 3 || backend.diagnostics != 0 {
		t.Fatalf("backend calls after block: gets=%d diagnostics=%d", backend.gets, backend.diagnostics)
	}
}

func TestCommandStoreCloseZeroesCachedValues(t *testing.T) {
	backend := &commandStoreBackend{values: map[string][]byte{"main:key": []byte("secret")}}
	store := NewCommandStore(backend)
	if _, err := store.Get(context.Background(), "main", "key"); err != nil {
		t.Fatal(err)
	}
	cached := store.values[commandSecretKey{profile: "main", key: "key"}]
	store.Close()
	for i, value := range cached {
		if value != 0 {
			t.Fatalf("cached byte %d was not zeroed", i)
		}
	}
	if !backend.closed {
		t.Fatal("underlying store was not closed")
	}
	if _, err := store.Get(context.Background(), "main", "key"); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("read after close = %v", err)
	}
}

type commandStoreBackend struct {
	values      map[string][]byte
	readErr     error
	gets        int
	diagnostics int
	closed      bool
}

func (s *commandStoreBackend) Get(_ context.Context, profile, key string) ([]byte, error) {
	s.gets++
	if s.readErr != nil {
		return nil, s.readErr
	}
	value, ok := s.values[profile+":"+key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *commandStoreBackend) Set(_ context.Context, profile, key string, value []byte) error {
	s.values[profile+":"+key] = append([]byte(nil), value...)
	return nil
}

func (s *commandStoreBackend) Delete(_ context.Context, profile, key string) error {
	delete(s.values, profile+":"+key)
	return nil
}

func (s *commandStoreBackend) CatalogDiagnostics(context.Context) (CatalogDiagnostics, error) {
	s.diagnostics++
	return CatalogDiagnostics{}, nil
}

func (s *commandStoreBackend) Close() { s.closed = true }
