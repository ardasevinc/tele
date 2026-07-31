package secrets

import (
	"context"
	"sync"
)

type LazyStore struct {
	Open func(context.Context) (Store, error)

	mu    sync.Mutex
	store Store
	err   error
}

func (s *LazyStore) Get(ctx context.Context, profile, key string) ([]byte, error) {
	store, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return store.Get(ctx, profile, key)
}

func (s *LazyStore) Set(ctx context.Context, profile, key string, value []byte) error {
	store, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	return store.Set(ctx, profile, key, value)
}

func (s *LazyStore) Delete(ctx context.Context, profile, key string) error {
	store, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	return store.Delete(ctx, profile, key)
}

func (s *LazyStore) resolve(ctx context.Context) (Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil || s.err != nil {
		return s.store, s.err
	}
	if s.Open == nil {
		s.err = &BackendError{Kind: ErrBackendUnavailable, Detail: "lazy backend opener is nil"}
		return nil, s.err
	}
	s.store, s.err = s.Open(ctx)
	return s.store, s.err
}
