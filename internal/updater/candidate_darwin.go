//go:build darwin

package updater

import "github.com/ardasevinc/tele/internal/buildtrust"

func requiresOfficialCandidate() bool { return true }

func verifyCandidate(path string) error {
	return buildtrust.VerifyOfficialDistribution(path)
}
