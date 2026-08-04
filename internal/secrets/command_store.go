package secrets

import (
	"context"
	"sync"
)

type commandSecretKey struct {
	profile string
	key     string
}

// CommandStore gives one command a coherent, bounded view of its secret backend.
// Successful reads are copied and reused. The first authorization failure stops
// later backend operations, and Close zeroes command-scoped secret copies.
type CommandStore struct {
	store Store

	mu      sync.Mutex
	values  map[commandSecretKey][]byte
	blocked error
	closed  bool
}

func NewCommandStore(store Store) *CommandStore {
	return &CommandStore{store: store, values: make(map[commandSecretKey][]byte)}
}

func (s *CommandStore) Get(ctx context.Context, profile, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return nil, err
	}
	cacheKey := commandSecretKey{profile: profile, key: key}
	if value, ok := s.values[cacheKey]; ok {
		return append([]byte(nil), value...), nil
	}
	value, err := s.store.Get(ctx, profile, key)
	if err != nil {
		s.block(err)
		return nil, err
	}
	defer zeroBytes(value)
	s.values[cacheKey] = append([]byte(nil), value...)
	return append([]byte(nil), value...), nil
}

func (s *CommandStore) Set(ctx context.Context, profile, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	if err := s.store.Set(ctx, profile, key, value); err != nil {
		s.block(err)
		return err
	}
	s.invalidate(commandSecretKey{profile: profile, key: key})
	return nil
}

func (s *CommandStore) Delete(ctx context.Context, profile, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	if err := s.store.Delete(ctx, profile, key); err != nil {
		s.block(err)
		return err
	}
	s.invalidate(commandSecretKey{profile: profile, key: key})
	return nil
}

func (s *CommandStore) BackendInfo() BackendInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if describer, ok := s.store.(Describer); ok {
		return describer.BackendInfo()
	}
	return BackendInfo{}
}

func (s *CommandStore) Snapshot(ctx context.Context) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return nil, err
	}
	snapshotter, ok := s.store.(Snapshotter)
	if !ok {
		return nil, &BackendError{Kind: ErrCatalogIncomplete, Detail: "backend does not expose an authoritative snapshot"}
	}
	snapshot, err := snapshotter.Snapshot(ctx)
	s.block(err)
	return snapshot, err
}

func (s *CommandStore) VaultDiagnostics(ctx context.Context) (VaultDiagnostics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return VaultDiagnostics{}, err
	}
	diagnoser, ok := s.store.(VaultDiagnoser)
	if !ok {
		return VaultDiagnostics{}, &BackendError{Kind: ErrBackendUnavailable, Detail: "backend is not a portable vault"}
	}
	diagnostics, err := diagnoser.VaultDiagnostics(ctx)
	s.block(err)
	return diagnostics, err
}

func (s *CommandStore) CatalogDiagnostics(ctx context.Context) (CatalogDiagnostics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return CatalogDiagnostics{}, err
	}
	diagnoser, ok := s.store.(CatalogDiagnoser)
	if !ok {
		return CatalogDiagnostics{}, &BackendError{Kind: ErrBackendUnavailable, Detail: "backend does not expose catalog diagnostics"}
	}
	diagnostics, err := diagnoser.CatalogDiagnostics(ctx)
	s.block(err)
	return diagnostics, err
}

func (s *CommandStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for key := range s.values {
		s.invalidate(key)
	}
	if closer, ok := s.store.(interface{ Close() }); ok {
		closer.Close()
	}
	s.closed = true
}

func (s *CommandStore) ready() error {
	if s.closed {
		return &BackendError{Kind: ErrBackendUnavailable, Detail: "secret store is closed"}
	}
	if s.blocked != nil {
		return s.blocked
	}
	if s.store == nil {
		return &BackendError{Kind: ErrBackendUnavailable, Detail: "secret store is nil"}
	}
	return nil
}

func (s *CommandStore) block(err error) {
	if s.blocked == nil && IsAccessBlocked(err) {
		s.blocked = err
	}
}

func (s *CommandStore) invalidate(key commandSecretKey) {
	if value, ok := s.values[key]; ok {
		zeroBytes(value)
		delete(s.values, key)
	}
}
