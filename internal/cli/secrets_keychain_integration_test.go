//go:build darwin && keychainintegration

package cli

import (
	"context"
	"testing"

	"github.com/ardasevinc/tele/internal/secrets"
)

func TestKeychainVaultMigrationRealLifecycle(t *testing.T) {
	testVaultNativeMigrationLifecycle(
		t,
		secrets.BackendKeychain,
		func(ctx context.Context, dataRoot, profile, instance string) (secrets.Store, error) {
			return secrets.OpenKeychain(ctx, dataRoot, profile, instance)
		},
		func(state *appState, ctx context.Context, instance, confirmation string) (purgeResult, error) {
			return state.purgeKeychain(ctx, instance, confirmation)
		},
	)
}
