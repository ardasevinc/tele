//go:build linux && secretserviceintegration

package cli

import (
	"context"
	"testing"

	"github.com/ardasevinc/tele/internal/secrets"
)

func TestSecretServiceVaultMigrationRealLifecycle(t *testing.T) {
	testVaultNativeMigrationLifecycle(
		t,
		secrets.BackendSecretService,
		func(ctx context.Context, dataRoot, profile, instance string) (secrets.Store, error) {
			return secrets.OpenSecretService(ctx, dataRoot, profile, instance)
		},
		func(state *appState, ctx context.Context, instance, confirmation string) (purgeResult, error) {
			return state.purgeSecretService(ctx, instance, confirmation)
		},
	)
}
