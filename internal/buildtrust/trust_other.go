//go:build !darwin

package buildtrust

func VerifyOfficial() error { return ErrNotOfficial }

func VerifyOfficialPath(string) error { return ErrNotOfficial }

func VerifyOfficialDistribution(string) error { return ErrNotOfficial }
