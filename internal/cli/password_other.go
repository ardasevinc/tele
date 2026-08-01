//go:build !darwin && !linux

package cli

import (
	"context"
	"errors"
)

func readPasswordContext(context.Context, int) ([]byte, error) {
	return nil, errors.New("interactive vault passphrase input is unsupported on this platform")
}
