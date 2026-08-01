//go:build !darwin

package secrets

func openLegacyKeychain(string) (Store, error) {
	return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: BackendKeychainLegacy}
}
