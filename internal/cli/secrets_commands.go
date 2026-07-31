package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/output"
	"github.com/ardasevinc/tele/internal/privatefs"
	"github.com/ardasevinc/tele/internal/secrets"
)

func secretsCommand(s *appState) *cobra.Command {
	cmd := &cobra.Command{Use: "secrets", Short: "Manage profile secret storage"}
	initCommand := &cobra.Command{
		Use:   "init --backend <backend>",
		Short: "Initialize secret storage for the active profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			backend, err := cmd.Flags().GetString("backend")
			if err != nil {
				return err
			}
			if secrets.BackendID(backend) != secrets.BackendVault {
				return fmt.Errorf("backend must be %s in this build", secrets.BackendVault)
			}
			result, err := s.initVault(cmd.Context())
			if err != nil {
				return err
			}
			return writeValue(s, result, func(w output.Writer) error {
				return w.Print(fmt.Sprintf("initialized %s for profile %s", safeHuman(string(result.Backend)), safeHuman(result.Profile)))
			})
		},
	}
	initCommand.Flags().String("backend", "", "secret backend ID")
	_ = initCommand.MarkFlagRequired("backend")
	cmd.AddCommand(initCommand)
	return cmd
}

type secretInitResult struct {
	Backend  secrets.BackendID `json:"backend"`
	Instance string            `json:"instance"`
	Profile  string            `json:"profile"`
}

func (s *appState) initVault(ctx context.Context) (secretInitResult, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return secretInitResult{}, err
	}
	profileName, profile, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return secretInitResult{}, err
	}
	if profile.Secrets != nil {
		return secretInitResult{}, fmt.Errorf("profile %q already selects secret backend %q", profileName, profile.Secrets.Backend)
	}
	passphrase, err := s.readVaultPassphrase(true)
	if err != nil {
		return secretInitResult{}, err
	}
	defer zeroSecret(passphrase)
	instance, err := secrets.NewVaultInstance()
	if err != nil {
		return secretInitResult{}, err
	}
	paths := mustPaths()
	vaultPath := secrets.VaultPath(paths.Data, profileName, instance)
	lockPath := filepath.Join(paths.Data, profileName, "secrets", "profile.lock")
	result := secretInitResult{Backend: secrets.BackendVault, Instance: instance, Profile: profileName}
	err = privatefs.WithLock(ctx, lockPath, func() error {
		latest, err := s.loadConfig()
		if err != nil {
			return err
		}
		_, latestProfile, err := latest.ResolveProfile(profileName)
		if err != nil {
			return err
		}
		if latestProfile.Secrets != nil {
			return fmt.Errorf("profile %q already selects secret backend %q", profileName, latestProfile.Secrets.Backend)
		}
		store, err := secrets.CreateVault(ctx, vaultPath, profileName, instance, passphrase)
		if err != nil {
			return err
		}
		store.Close()
		return config.Update(ctx, s.cfgPath, func(current *config.Config) error {
			_, currentProfile, err := current.ResolveProfile(profileName)
			if err != nil {
				return err
			}
			if currentProfile.Secrets != nil {
				return fmt.Errorf("profile %q selected another secret backend during initialization", profileName)
			}
			if _, err := current.EnsureProfile(profileName); err != nil {
				return err
			}
			currentProfile.Secrets = &config.SecretBackend{Backend: string(secrets.BackendVault), Instance: instance}
			current.Profiles[profileName] = currentProfile
			return nil
		})
	})
	return result, err
}

func (s *appState) readVaultPassphrase(confirm bool) ([]byte, error) {
	sources := 0
	if s.vaultPassphraseFD >= 0 {
		sources++
	}
	if s.vaultPassphraseFile != "" {
		sources++
	}
	if sources > 1 {
		return nil, fmt.Errorf("--vault-passphrase-fd and --vault-passphrase-file are mutually exclusive")
	}
	if s.vaultPassphraseFD >= 0 {
		return secrets.ReadPassphraseFD(s.vaultPassphraseFD)
	}
	if s.vaultPassphraseFile != "" {
		return secrets.ReadPassphraseFile(s.vaultPassphraseFile)
	}
	if s.json || s.jsonl {
		return nil, fmt.Errorf("vault passphrase source required in machine-output mode")
	}
	return readVaultPassphraseTTY(confirm)
}

func readVaultPassphraseTTY(confirm bool) ([]byte, error) {
	// #nosec G304 -- /dev/tty is the controlling terminal, never redirected stdin.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("vault passphrase source required: no controlling TTY: %w", err)
	}
	defer tty.Close()
	if !term.IsTerminal(int(tty.Fd())) {
		return nil, fmt.Errorf("vault passphrase source required: controlling TTY is unavailable")
	}
	if confirm {
		if _, err := fmt.Fprintln(tty, "warning: losing this passphrase permanently loses access to the vault"); err != nil {
			return nil, err
		}
	}
	first, err := readHiddenTTY(tty, "vault passphrase: ")
	if err != nil {
		return nil, err
	}
	if !confirm {
		return first, nil
	}
	second, err := readHiddenTTY(tty, "confirm vault passphrase: ")
	if err != nil {
		zeroSecret(first)
		return nil, err
	}
	defer zeroSecret(second)
	if !bytes.Equal(first, second) {
		zeroSecret(first)
		return nil, fmt.Errorf("vault passphrases do not match")
	}
	return first, nil
}

func readHiddenTTY(tty *os.File, label string) ([]byte, error) {
	if _, err := fmt.Fprint(tty, label); err != nil {
		return nil, err
	}
	value, err := term.ReadPassword(int(tty.Fd()))
	_, _ = fmt.Fprintln(tty)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 {
		return nil, errors.New("vault passphrase must not be empty")
	}
	if len(value) > secrets.MaxPassphraseSize {
		zeroSecret(value)
		return nil, fmt.Errorf("vault passphrase exceeds %d bytes", secrets.MaxPassphraseSize)
	}
	return value, nil
}

func zeroSecret(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
