//go:build darwin

package buildtrust

import (
	"errors"
	"testing"
)

func TestUnsignedTestBinaryIsNotOfficial(t *testing.T) {
	if err := verifyOfficial(); !errors.Is(err, ErrNotOfficial) {
		t.Fatalf("verifyOfficial = %v, want ErrNotOfficial", err)
	}
}
