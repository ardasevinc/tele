//go:build !darwin

package secrets

func connectNativeKeychain() (nativeKeychainAPI, error) {
	return nil, &BackendError{Kind: ErrBackendUnavailable, Backend: BackendKeychain}
}
