//go:build !darwin

package secrets

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedStoreFailsEveryOperationTruthfully(t *testing.T) {
	store := NewStore()
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "get", run: func() error { _, err := store.Get(context.Background(), "main", "api-hash"); return err }},
		{name: "set", run: func() error { return store.Set(context.Background(), "main", "api-hash", []byte("secret")) }},
		{name: "delete", run: func() error { return store.Delete(context.Background(), "main", "api-hash") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if !errors.Is(err, ErrBackendUnavailable) {
				t.Fatalf("error = %v, want ErrBackendUnavailable", err)
			}
			if errors.Is(err, ErrNotFound) {
				t.Fatalf("unsupported backend masqueraded as missing secret: %v", err)
			}
		})
	}
}

func TestUnsupportedStoreDescribesUnavailableBackend(t *testing.T) {
	store := NewStore()
	describer, ok := store.(Describer)
	if !ok {
		t.Fatal("unsupported store does not describe its backend")
	}
	info := describer.BackendInfo()
	if info.Supported || info.Name == "" {
		t.Fatalf("backend info = %+v", info)
	}
}
