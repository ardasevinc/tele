//go:build !darwin

package cli

import "github.com/ardasevinc/tele/internal/secrets"

func newProfileBackend() secrets.BackendID {
	return ""
}
