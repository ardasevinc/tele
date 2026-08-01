//go:build darwin

package secrets

func openLegacyKeychain(dataRoot string) (Store, error) {
	return KeychainStore{dataRoot: dataRoot}, nil
}
