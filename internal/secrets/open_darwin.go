//go:build darwin

package secrets

func openLegacyKeychain() (Store, error) {
	return KeychainStore{}, nil
}
