//go:build !darwin

package buildtrust

func VerifyOfficial() error { return ErrNotOfficial }
