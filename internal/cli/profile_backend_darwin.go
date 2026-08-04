//go:build darwin

package cli

import "github.com/ardasevinc/tele/internal/secrets"

func (s *appState) newProfileBackend() secrets.BackendID {
	if s.requireOfficialKeychain() != nil {
		return ""
	}
	return secrets.BackendKeychain
}
