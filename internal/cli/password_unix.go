//go:build darwin || linux

package cli

import (
	"context"
	"errors"
	"io"
	"math"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/ardasevinc/tele/internal/secrets"
)

func readPasswordContext(ctx context.Context, fd int) (result []byte, retErr error) {
	if fd < 0 || fd > math.MaxInt32 {
		return nil, errors.New("terminal file descriptor is out of range")
	}
	pollFD := int32(fd) // #nosec G115 -- the descriptor range is checked above.
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer func() { _ = term.Restore(fd, state) }()

	value := make([]byte, 0, 64)
	defer func() {
		if retErr != nil {
			zeroSecret(value)
		}
	}()
	buffer := []byte{0}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pollTimeout := 50
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return nil, context.DeadlineExceeded
			}
			if remaining < time.Duration(pollTimeout)*time.Millisecond {
				pollTimeout = max(int(remaining.Milliseconds()), 1)
			}
		}
		poll := []unix.PollFd{{Fd: pollFD, Events: unix.POLLIN}}
		ready, pollErr := unix.Poll(poll, pollTimeout)
		if errors.Is(pollErr, unix.EINTR) {
			continue
		}
		if pollErr != nil {
			return nil, pollErr
		}
		if ready == 0 {
			continue
		}
		if poll[0].Revents&unix.POLLIN == 0 && poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return nil, io.EOF
		}
		read, readErr := unix.Read(fd, buffer)
		if readErr != nil {
			return nil, readErr
		}
		if read == 0 {
			return nil, io.EOF
		}
		switch buffer[0] {
		case '\r', '\n':
			return value, nil
		case '\b', 0x7f:
			if len(value) > 0 {
				value = value[:len(value)-1]
			}
		case 0x03:
			return nil, context.Canceled
		case 0x04:
			if len(value) == 0 {
				return nil, io.EOF
			}
			return value, nil
		default:
			value = append(value, buffer[0])
			if len(value) > secrets.MaxPassphraseSize {
				return nil, errors.New("vault passphrase exceeds size limit")
			}
		}
	}
}
