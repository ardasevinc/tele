//go:build !darwin

package cli

import "github.com/ardasevinc/tele/internal/secrets"

func (*appState) newProfileBackend() secrets.BackendID {
	return ""
}
