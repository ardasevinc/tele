package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

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
	migrateCommand := &cobra.Command{
		Use:   "migrate --to <backend>",
		Short: "Copy and verify all secrets before switching backends",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			to, err := cmd.Flags().GetString("to")
			if err != nil {
				return err
			}
			receipt, err := s.migrateSecrets(cmd.Context(), secrets.BackendID(to))
			if err != nil {
				return err
			}
			return writeValue(s, receipt, func(w output.Writer) error {
				return w.Print(fmt.Sprintf("migrated %d secrets from %s to %s", receipt.KeyCount, safeHuman(string(receipt.Source.Backend)), safeHuman(string(receipt.Target.Backend))))
			})
		},
	}
	migrateCommand.Flags().String("to", "", "target secret backend ID")
	_ = migrateCommand.MarkFlagRequired("to")
	cmd.AddCommand(migrateCommand)
	var confirmInstance string
	purgeCommand := &cobra.Command{
		Use:   "purge <instance>",
		Short: "Preview or purge a retained inactive vault instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := s.purgeVault(cmd.Context(), args[0], confirmInstance)
			if err != nil {
				return err
			}
			return writeValue(s, result, func(w output.Writer) error {
				action := "would purge"
				if result.Purged {
					action = "purged"
				}
				return w.Print(fmt.Sprintf("%s inactive vault %s", action, safeHuman(result.Instance)))
			})
		},
	}
	purgeCommand.Flags().StringVar(&confirmInstance, "confirm-instance", "", "delete only when this exactly matches the instance UUID")
	cmd.AddCommand(purgeCommand)
	return cmd
}

type secretInitResult struct {
	Backend  secrets.BackendID `json:"backend"`
	Instance string            `json:"instance"`
	Profile  string            `json:"profile"`
}

type migrationEndpoint struct {
	Backend  secrets.BackendID `json:"backend"`
	Instance string            `json:"instance,omitempty"`
}

type migrationReceipt struct {
	Schema     string            `json:"schema"`
	Profile    string            `json:"profile"`
	Source     migrationEndpoint `json:"source"`
	Target     migrationEndpoint `json:"target"`
	KeyCount   int               `json:"key_count"`
	VerifiedAt string            `json:"verified_at"`
}

type purgeResult struct {
	Profile  string            `json:"profile"`
	Backend  secrets.BackendID `json:"backend"`
	Instance string            `json:"instance"`
	Path     string            `json:"path"`
	Active   bool              `json:"active"`
	Purged   bool              `json:"purged"`
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

func (s *appState) migrateSecrets(ctx context.Context, targetBackend secrets.BackendID) (migrationReceipt, error) {
	if targetBackend != secrets.BackendVault {
		return migrationReceipt{}, fmt.Errorf("target backend must be %s in this build", secrets.BackendVault)
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return migrationReceipt{}, err
	}
	profileName, profile, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return migrationReceipt{}, err
	}
	if profile.Secrets == nil {
		return migrationReceipt{}, &secrets.BackendError{Kind: secrets.ErrBackendUnconfigured, Detail: "migration source is not selected"}
	}
	passphrase, err := s.readVaultPassphrase(false)
	if err != nil {
		return migrationReceipt{}, err
	}
	defer zeroSecret(passphrase)
	paths := mustPaths()
	lockPath := filepath.Join(paths.Data, profileName, "secrets", "profile.lock")
	var receipt migrationReceipt
	err = privatefs.WithLock(ctx, lockPath, func() error {
		latest, err := s.loadConfig()
		if err != nil {
			return err
		}
		_, latestProfile, err := latest.ResolveProfile(profileName)
		if err != nil {
			return err
		}
		if latestProfile.Secrets == nil || latestProfile.Secrets.Backend != profile.Secrets.Backend || latestProfile.Secrets.Instance != profile.Secrets.Instance {
			return &secrets.BackendError{Kind: secrets.ErrMigrationIncomplete, Detail: "source selector changed before migration"}
		}
		sourceSelection := secrets.Selection{Backend: secrets.BackendID(profile.Secrets.Backend), Instance: profile.Secrets.Instance}
		source, err := secrets.Open(sourceSelection, secrets.OpenOptions{DataRoot: paths.Data, Profile: profileName, Passphrase: passphrase})
		if err != nil {
			return err
		}
		defer closeSecretStore(source)
		snapshotter, ok := source.(secrets.Snapshotter)
		if !ok {
			return &secrets.BackendError{Kind: secrets.ErrCatalogIncomplete, Backend: sourceSelection.Backend, Detail: "source has no authoritative catalog"}
		}
		snapshot, err := snapshotter.Snapshot(ctx)
		if err != nil {
			return err
		}
		defer zeroSnapshot(snapshot)
		targetInstance, err := secrets.NewVaultInstance()
		if err != nil {
			return err
		}
		target, err := secrets.CreateVault(ctx, secrets.VaultPath(paths.Data, profileName, targetInstance), profileName, targetInstance, passphrase)
		if err != nil {
			return err
		}
		defer target.Close()
		keys := make([]string, 0, len(snapshot))
		for key := range snapshot {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := target.Set(ctx, profileName, key, snapshot[key]); err != nil {
				return &secrets.BackendError{Kind: secrets.ErrMigrationIncomplete, Backend: targetBackend, Detail: "target write failed"}
			}
		}
		for _, key := range keys {
			value, err := target.Get(ctx, profileName, key)
			if err != nil || !bytes.Equal(value, snapshot[key]) {
				zeroSecret(value)
				return &secrets.BackendError{Kind: secrets.ErrMigrationIncomplete, Backend: targetBackend, Detail: "target readback verification failed"}
			}
			zeroSecret(value)
		}
		receipt = migrationReceipt{
			Schema: "tele/secret-migration/v1", Profile: profileName,
			Source:   migrationEndpoint{Backend: sourceSelection.Backend, Instance: sourceSelection.Instance},
			Target:   migrationEndpoint{Backend: targetBackend, Instance: targetInstance},
			KeyCount: len(keys), VerifiedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		encoded, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return err
		}
		receiptPath := filepath.Join(paths.Data, profileName, "secrets", "migrations", targetInstance+".json")
		if err := privatefs.AtomicWriteFile(receiptPath, encoded); err != nil {
			return &secrets.BackendError{Kind: secrets.ErrMigrationIncomplete, Detail: "migration receipt write failed"}
		}
		return config.Update(ctx, s.cfgPath, func(current *config.Config) error {
			_, currentProfile, err := current.ResolveProfile(profileName)
			if err != nil {
				return err
			}
			if currentProfile.Secrets == nil || currentProfile.Secrets.Backend != profile.Secrets.Backend || currentProfile.Secrets.Instance != profile.Secrets.Instance {
				return &secrets.BackendError{Kind: secrets.ErrMigrationIncomplete, Detail: "source selector changed before activation"}
			}
			currentProfile.Secrets = &config.SecretBackend{Backend: string(targetBackend), Instance: targetInstance}
			current.Profiles[profileName] = currentProfile
			return nil
		})
	})
	return receipt, err
}

func (s *appState) purgeVault(ctx context.Context, instance, confirmation string) (purgeResult, error) {
	if err := secrets.ValidateVaultInstance(instance); err != nil {
		return purgeResult{}, err
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return purgeResult{}, err
	}
	profileName, profile, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return purgeResult{}, err
	}
	paths := mustPaths()
	path := secrets.VaultPath(paths.Data, profileName, instance)
	result := purgeResult{Profile: profileName, Backend: secrets.BackendVault, Instance: instance, Path: path}
	if profile.Secrets != nil && profile.Secrets.Backend == string(secrets.BackendVault) && profile.Secrets.Instance == instance {
		result.Active = true
		return result, fmt.Errorf("refusing to purge active vault instance %q", instance)
	}
	if err := secrets.InspectVaultInstance(path, instance); err != nil {
		return result, err
	}
	if confirmation == "" {
		return result, nil
	}
	if confirmation != instance {
		return result, fmt.Errorf("--confirm-instance must exactly match %q", instance)
	}
	lockPath := filepath.Join(paths.Data, profileName, "secrets", "profile.lock")
	err = privatefs.WithLock(ctx, lockPath, func() error {
		latest, err := s.loadConfig()
		if err != nil {
			return err
		}
		_, latestProfile, err := latest.ResolveProfile(profileName)
		if err != nil {
			return err
		}
		if latestProfile.Secrets != nil && latestProfile.Secrets.Backend == string(secrets.BackendVault) && latestProfile.Secrets.Instance == instance {
			return fmt.Errorf("refusing to purge active vault instance %q", instance)
		}
		return secrets.PurgeVault(ctx, path, instance)
	})
	if err != nil {
		return result, err
	}
	result.Purged = true
	return result, nil
}

func closeSecretStore(store secrets.Store) {
	if closer, ok := store.(interface{ Close() }); ok {
		closer.Close()
	}
}

func zeroSnapshot(snapshot map[string][]byte) {
	for _, value := range snapshot {
		zeroSecret(value)
	}
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
