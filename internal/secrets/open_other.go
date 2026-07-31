//go:build !darwin

package secrets

func openLegacyKeychain() (Store, error) {
	return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: BackendKeychainLegacy}
}
