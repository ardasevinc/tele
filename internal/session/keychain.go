package session

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/ardasevinc/tele/internal/privatefs"
	"github.com/ardasevinc/tele/internal/secrets"
	gotdsession "github.com/gotd/td/session"
)

const (
	Key           = "mtproto-session"
	EncryptionKey = "mtproto-session-key"
)

type KeychainStorage struct {
	Profile string
	Store   secrets.Store
	Path    string
}

func (s KeychainStorage) LoadSession(ctx context.Context) ([]byte, error) {
	if s.Path == "" {
		return nil, fmt.Errorf("session storage path is required")
	}
	var data []byte
	err := privatefs.WithLock(ctx, s.Path+".lock", func() error {
		var err error
		data, err = s.loadSession(ctx)
		return err
	})
	return data, err
}

// InspectSession reads and decrypts session state without repairing permissions,
// acquiring a mutation lock, or creating missing key material.
func (s KeychainStorage) InspectSession(ctx context.Context) ([]byte, error) {
	ciphertext, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, gotdsession.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	key, err := s.key(ctx, false)
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, gotdsession.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decrypt(key, ciphertext)
}

func (s KeychainStorage) loadSession(ctx context.Context) ([]byte, error) {
	if err := privatefs.RepairFile(s.Path); err != nil {
		return nil, err
	}
	ciphertext, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, gotdsession.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	key, err := s.key(ctx, false)
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, gotdsession.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decrypt(key, ciphertext)
}

func (s KeychainStorage) StoreSession(ctx context.Context, data []byte) error {
	if s.Path == "" {
		return fmt.Errorf("session storage path is required")
	}
	return privatefs.WithLock(ctx, s.Path+".lock", func() error {
		return s.storeSession(ctx, data)
	})
}

func (s KeychainStorage) storeSession(ctx context.Context, data []byte) error {
	key, err := s.key(ctx, true)
	if err != nil {
		return err
	}
	ciphertext, err := encrypt(key, data)
	if err != nil {
		return err
	}
	return privatefs.AtomicWriteFile(s.Path, ciphertext)
}

func (s KeychainStorage) Delete(ctx context.Context) error {
	if s.Path == "" {
		return s.delete(ctx)
	}
	return privatefs.WithLock(ctx, s.Path+".lock", func() error {
		return s.delete(ctx)
	})
}

func (s KeychainStorage) delete(ctx context.Context) error {
	var errs []error
	if s.Path != "" {
		if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := s.Store.Delete(ctx, s.Profile, EncryptionKey); err != nil {
		errs = append(errs, err)
	}
	if err := s.Store.Delete(ctx, s.Profile, Key); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s KeychainStorage) key(ctx context.Context, create bool) ([]byte, error) {
	encoded, err := s.Store.Get(ctx, s.Profile, EncryptionKey)
	if errors.Is(err, secrets.ErrNotFound) && create {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		encoded := []byte(base64.StdEncoding.EncodeToString(key))
		if err := s.Store.Set(ctx, s.Profile, EncryptionKey, encoded); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	key := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(key, encoded)
	if err != nil {
		return nil, err
	}
	key = key[:n]
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid session encryption key length %d", len(key))
	}
	return key, nil
}

func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("session ciphertext is too short")
	}
	nonce := ciphertext[:gcm.NonceSize()]
	body := ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, nil)
}
