package cli

import (
	"fmt"

	"github.com/ardasevinc/tele/internal/buildtrust"
	"github.com/ardasevinc/tele/internal/secrets"
)

func (s *appState) requireOfficialKeychain() error {
	check := buildtrust.VerifyOfficial
	if s.officialKeychainCheck != nil {
		check = s.officialKeychainCheck
	}
	if err := check(); err != nil {
		return &secrets.BackendError{
			Kind:    secrets.ErrBackendUnavailable,
			Backend: secrets.BackendKeychain,
			Detail:  fmt.Sprintf("official Developer ID-signed Tele build required; use %s with this build", secrets.BackendVault),
		}
	}
	return nil
}

func isKeychainBackend(backend secrets.BackendID) bool {
	return backend == secrets.BackendKeychain || backend == secrets.BackendKeychainLegacy
}
