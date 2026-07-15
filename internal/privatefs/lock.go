package privatefs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
)

var processLocks sync.Map

type processLock struct {
	token chan struct{}
}

func WithLock(ctx context.Context, path string, fn func() error) (retErr error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	local := lockForPath(path)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-local.token:
	}
	defer func() { local.token <- struct{}{} }()
	release, err := acquireLock(ctx, path)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, release()) }()
	return fn()
}

func lockForPath(path string) *processLock {
	created := &processLock{token: make(chan struct{}, 1)}
	created.token <- struct{}{}
	lock, _ := processLocks.LoadOrStore(path, created)
	return lock.(*processLock)
}
