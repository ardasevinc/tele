//go:build darwin

package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const (
	nativeKeychainService = "tele-v1"
	nativeValuePrefix     = "tele-base64-v1:"
)

type securityCLIKeychain struct{}

func connectNativeKeychain() (nativeKeychainAPI, error) {
	return securityCLIKeychain{}, nil
}

func (securityCLIKeychain) Create(ctx context.Context, account string, value []byte) error {
	return writeSecurityItem(ctx, account, value, false)
}

func (securityCLIKeychain) Replace(ctx context.Context, account string, value []byte) error {
	return writeSecurityItem(ctx, account, value, true)
}

func writeSecurityItem(ctx context.Context, account string, value []byte, replace bool) error {
	encoded := nativeValuePrefix + base64.StdEncoding.EncodeToString(value)
	update := ""
	if replace {
		update = " -U"
	}
	command := fmt.Sprintf("add-generic-password%s -s %s -a %s -w %s\n", update, securityQuote(nativeKeychainService), securityQuote(account), securityQuote(encoded))
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(command)
	var stderr bytes.Buffer
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return classifySecurityCLIError(err, stderr.String())
	}
	return nil
}

func securityQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (securityCLIKeychain) Get(ctx context.Context, account string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", nativeKeychainService, "-a", account, "-w")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, classifySecurityCLIError(err, string(output))
	}
	encoded := strings.TrimSpace(string(output))
	if !strings.HasPrefix(encoded, nativeValuePrefix) {
		return nil, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "keychain item encoding is invalid"}
	}
	value, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, nativeValuePrefix))
	if err != nil {
		return nil, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "keychain item encoding is invalid"}
	}
	return value, nil
}

func (securityCLIKeychain) Delete(ctx context.Context, account string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-s", nativeKeychainService, "-a", account)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return classifySecurityCLIError(err, string(output))
	}
	return nil
}

func classifySecurityCLIError(err error, output string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "could not be found"):
		return ErrNotFound
	case strings.Contains(lower, "user interaction is not allowed"), strings.Contains(lower, "user canceled"), strings.Contains(lower, "authentication failed"):
		return &BackendError{Kind: ErrBackendLocked, Backend: BackendKeychain}
	default:
		return fmt.Errorf("security command failed: %w", err)
	}
}
