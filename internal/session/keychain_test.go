package session

import (
	"context"
	"errors"
	"testing"

	"github.com/ardasevinc/tele/internal/secrets"
	gotdsession "github.com/gotd/td/session"
)

type missingStore struct{}

func (missingStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, secrets.ErrNotFound
}

func (missingStore) Set(context.Context, string, string, []byte) error {
	return nil
}

func (missingStore) Delete(context.Context, string, string) error {
	return nil
}

func TestLoadSessionMapsMissingSecretToGotdErrNotFound(t *testing.T) {
	storage := KeychainStorage{Profile: "test", Store: missingStore{}}
	_, err := storage.LoadSession(context.Background())
	if !errors.Is(err, gotdsession.ErrNotFound) {
		t.Fatalf("err = %v, want %v", err, gotdsession.ErrNotFound)
	}
}
