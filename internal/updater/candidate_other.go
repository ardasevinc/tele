//go:build !darwin

package updater

func requiresOfficialCandidate() bool { return false }

func verifyCandidate(string) error { return nil }
