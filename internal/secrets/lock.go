package secrets

import (
	"context"
	"path/filepath"

	"github.com/ardasevinc/tele/internal/privatefs"
)

type profileLockContextKey struct{}

func ProfileLockPath(dataRoot, profile string) string {
	return filepath.Join(dataRoot, profile, "secrets", "profile.lock")
}

func WithProfileLock(ctx context.Context, dataRoot, profile string, fn func(context.Context) error) error {
	return withProfileLockPath(ctx, ProfileLockPath(dataRoot, profile), fn)
}

func withProfileLockPath(ctx context.Context, lockPath string, fn func(context.Context) error) error {
	absolute, err := filepath.Abs(lockPath)
	if err != nil {
		return err
	}
	if held, _ := ctx.Value(profileLockContextKey{}).(string); held == absolute {
		return fn(ctx)
	}
	return privatefs.WithLock(ctx, absolute, func() error {
		return fn(context.WithValue(ctx, profileLockContextKey{}, absolute))
	})
}
