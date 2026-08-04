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

	"github.com/ardasevinc/tele/internal/botfactory"
	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/output"
	"github.com/ardasevinc/tele/internal/privatefs"
	"github.com/ardasevinc/tele/internal/secrets"
	"github.com/ardasevinc/tele/internal/session"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
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
			result, err := s.initSecrets(cmd.Context(), secrets.BackendID(backend))
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
	var purgeBackend string
	purgeCommand := &cobra.Command{
		Use:   "purge --backend <backend> <instance>",
		Short: "Preview or purge a retained inactive secret backend instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := s.purgeSecrets(cmd.Context(), secrets.BackendID(purgeBackend), args[0], confirmInstance)
			if err != nil {
				return err
			}
			return writeValue(s, result, func(w output.Writer) error {
				action := "would purge"
				if result.Purged {
					action = "purged"
				}
				return w.Print(fmt.Sprintf("%s inactive %s instance %s", action, safeHuman(string(result.Backend)), safeHuman(result.Instance)))
			})
		},
	}
	purgeCommand.Flags().StringVar(&purgeBackend, "backend", "", "secret backend ID")
	_ = purgeCommand.MarkFlagRequired("backend")
	purgeCommand.Flags().StringVar(&confirmInstance, "confirm-instance", "", "delete only when this exactly matches the instance UUID")
	cmd.AddCommand(purgeCommand)
	return cmd
}

func (s *appState) purgeSecrets(ctx context.Context, backend secrets.BackendID, instance, confirmation string) (purgeResult, error) {
	switch backend {
	case secrets.BackendVault:
		return s.purgeVault(ctx, instance, confirmation)
	case secrets.BackendSecretService:
		return s.purgeSecretService(ctx, instance, confirmation)
	case secrets.BackendKeychain:
		return s.purgeKeychain(ctx, instance, confirmation)
	default:
		return purgeResult{}, fmt.Errorf("backend must be %s, %s, or %s", secrets.BackendVault, secrets.BackendSecretService, secrets.BackendKeychain)
	}
}

func (s *appState) initSecrets(ctx context.Context, backend secrets.BackendID) (secretInitResult, error) {
	switch backend {
	case secrets.BackendVault:
		return s.initVault(ctx)
	case secrets.BackendSecretService:
		return s.initSecretService(ctx)
	case secrets.BackendKeychain:
		return s.initKeychain(ctx)
	default:
		return secretInitResult{}, fmt.Errorf("backend must be %s, %s, or %s", secrets.BackendVault, secrets.BackendSecretService, secrets.BackendKeychain)
	}
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
	passphrase, err := s.readVaultPassphrase(ctx, true)
	if err != nil {
		return secretInitResult{}, err
	}
	defer zeroSecret(passphrase)
	instance, err := secrets.NewVaultInstance()
	if err != nil {
		return secretInitResult{}, err
	}
	paths, err := s.paths()
	if err != nil {
		return secretInitResult{}, err
	}
	vaultPath := secrets.VaultPath(paths.Data, profileName, instance)
	result := secretInitResult{Backend: secrets.BackendVault, Instance: instance, Profile: profileName}
	err = secrets.WithProfileLock(ctx, paths.Data, profileName, func(ctx context.Context) error {
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
		return s.updateConfig(ctx, func(current *config.Config) error {
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

func (s *appState) initSecretService(ctx context.Context) (secretInitResult, error) {
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
	instance, err := secrets.NewVaultInstance()
	if err != nil {
		return secretInitResult{}, err
	}
	paths, err := s.paths()
	if err != nil {
		return secretInitResult{}, err
	}
	result := secretInitResult{Backend: secrets.BackendSecretService, Instance: instance, Profile: profileName}
	err = secrets.WithProfileLock(ctx, paths.Data, profileName, func(ctx context.Context) error {
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
		store, err := secrets.InitSecretService(ctx, paths.Data, profileName, instance)
		if err != nil {
			return err
		}
		store.Close()
		return s.updateConfig(ctx, func(current *config.Config) error {
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
			currentProfile.Secrets = &config.SecretBackend{Backend: string(secrets.BackendSecretService), Instance: instance}
			current.Profiles[profileName] = currentProfile
			return nil
		})
	})
	return result, err
}

func (s *appState) initKeychain(ctx context.Context) (secretInitResult, error) {
	if err := s.requireOfficialKeychain(); err != nil {
		return secretInitResult{}, err
	}
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
	instance, err := secrets.NewVaultInstance()
	if err != nil {
		return secretInitResult{}, err
	}
	paths, err := s.paths()
	if err != nil {
		return secretInitResult{}, err
	}
	result := secretInitResult{Backend: secrets.BackendKeychain, Instance: instance, Profile: profileName}
	err = secrets.WithProfileLock(ctx, paths.Data, profileName, func(ctx context.Context) error {
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
		store, err := secrets.InitKeychain(ctx, paths.Data, profileName, instance)
		if err != nil {
			return err
		}
		store.Close()
		return s.updateConfig(ctx, func(current *config.Config) error {
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
			currentProfile.Secrets = &config.SecretBackend{Backend: string(secrets.BackendKeychain), Instance: instance}
			current.Profiles[profileName] = currentProfile
			return nil
		})
	})
	return result, err
}

func (s *appState) migrateSecrets(ctx context.Context, targetBackend secrets.BackendID) (migrationReceipt, error) {
	if targetBackend != secrets.BackendVault && targetBackend != secrets.BackendSecretService && targetBackend != secrets.BackendKeychain {
		return migrationReceipt{}, fmt.Errorf("target backend must be %s, %s, or %s", secrets.BackendVault, secrets.BackendSecretService, secrets.BackendKeychain)
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return migrationReceipt{}, err
	}
	profileName, profile, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return migrationReceipt{}, err
	}
	sourceConfigured := selectionFromProfile(profile)
	sourceSelection, err := secrets.EffectiveSelection(sourceConfigured)
	if err != nil {
		return migrationReceipt{}, err
	}
	if isKeychainBackend(sourceSelection.Backend) || targetBackend == secrets.BackendKeychain {
		if err := s.requireOfficialKeychain(); err != nil {
			return migrationReceipt{}, err
		}
	}
	var passphrase []byte
	sourceBackend := sourceSelection.Backend
	if sourceBackend == secrets.BackendVault || targetBackend == secrets.BackendVault {
		passphrase, err = s.readVaultPassphrase(ctx, false)
		if err != nil {
			return migrationReceipt{}, err
		}
		defer zeroSecret(passphrase)
	}
	paths, err := s.paths()
	if err != nil {
		return migrationReceipt{}, err
	}
	var receipt migrationReceipt
	err = secrets.WithProfileLock(ctx, paths.Data, profileName, func(ctx context.Context) error {
		latest, err := s.loadConfig()
		if err != nil {
			return err
		}
		_, latestProfile, err := latest.ResolveProfile(profileName)
		if err != nil {
			return err
		}
		if !profileStillSelects(latestProfile, sourceConfigured) {
			return &secrets.BackendError{Kind: secrets.ErrMigrationIncomplete, Detail: "source selector changed before migration"}
		}
		source, err := secrets.Open(ctx, sourceSelection, secrets.OpenOptions{DataRoot: paths.Data, Profile: profileName, Passphrase: passphrase})
		if err != nil {
			return err
		}
		defer closeSecretStore(source)
		snapshot, err := s.migrationSnapshot(ctx, sourceSelection, source, profileName)
		if err != nil {
			return err
		}
		defer zeroSnapshot(snapshot)
		targetInstance, err := secrets.NewVaultInstance()
		if err != nil {
			return err
		}
		target, err := createMigrationTarget(ctx, targetBackend, paths.Data, profileName, targetInstance, passphrase)
		if err != nil {
			return err
		}
		defer closeSecretStore(target)
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
		return s.updateConfig(ctx, func(current *config.Config) error {
			_, currentProfile, err := current.ResolveProfile(profileName)
			if err != nil {
				return err
			}
			if !profileStillSelects(currentProfile, sourceConfigured) {
				return &secrets.BackendError{Kind: secrets.ErrMigrationIncomplete, Detail: "source selector changed before activation"}
			}
			currentProfile.Secrets = &config.SecretBackend{Backend: string(targetBackend), Instance: targetInstance}
			current.Profiles[profileName] = currentProfile
			return nil
		})
	})
	return receipt, err
}

func selectionFromProfile(profile config.Profile) secrets.Selection {
	if profile.Secrets == nil {
		return secrets.Selection{}
	}
	return secrets.Selection{
		Backend:  secrets.BackendID(profile.Secrets.Backend),
		Instance: profile.Secrets.Instance,
	}
}

func profileStillSelects(profile config.Profile, expected secrets.Selection) bool {
	if expected.Backend == "" {
		return profile.Secrets == nil
	}
	return profile.Secrets != nil &&
		profile.Secrets.Backend == string(expected.Backend) &&
		profile.Secrets.Instance == expected.Instance
}

func (s *appState) migrationSnapshot(
	ctx context.Context,
	selection secrets.Selection,
	source secrets.Store,
	profile string,
) (map[string][]byte, error) {
	if selection.Backend != secrets.BackendKeychainLegacy {
		snapshotter, ok := source.(secrets.Snapshotter)
		if !ok {
			return nil, &secrets.BackendError{Kind: secrets.ErrCatalogIncomplete, Backend: selection.Backend, Detail: "source has no authoritative catalog"}
		}
		return snapshotter.Snapshot(ctx)
	}
	discoverer, err := s.botDiscoverer()
	if err != nil {
		return nil, err
	}
	inventory, err := s.botsStore()
	if err != nil {
		return nil, err
	}
	dynamicKeys, err := botfactory.VerifyLegacyBotCatalog(
		ctx,
		source,
		s.botManagerAPI(),
		discoverer,
		inventory,
		profile,
		secrets.BackendDisplayName(selection.Backend),
	)
	if err != nil {
		return nil, err
	}
	keys := append([]string{
		tgapp.APIHashSecretKey,
		tgapp.AuthPendingSecretKey,
		session.EncryptionKey,
		botfactory.ManagerSecretKey,
	}, dynamicKeys...)
	return snapshotKnownKeys(ctx, source, profile, keys)
}

func snapshotKnownKeys(ctx context.Context, source secrets.Store, profile string, keys []string) (map[string][]byte, error) {
	sort.Strings(keys)
	snapshot := make(map[string][]byte, len(keys))
	previous := ""
	for _, key := range keys {
		if key == previous {
			continue
		}
		previous = key
		value, err := source.Get(ctx, profile, key)
		if errors.Is(err, secrets.ErrNotFound) {
			continue
		}
		if err != nil {
			zeroSnapshot(snapshot)
			return nil, &secrets.BackendError{
				Kind:    secrets.ErrCatalogIncomplete,
				Backend: secrets.BackendKeychainLegacy,
				Detail:  fmt.Sprintf("known key %q could not be read", key),
			}
		}
		snapshot[key] = value
	}
	return snapshot, nil
}

func createMigrationTarget(ctx context.Context, backend secrets.BackendID, dataRoot, profile, instance string, passphrase []byte) (secrets.Store, error) {
	switch backend {
	case secrets.BackendVault:
		return secrets.CreateVault(ctx, secrets.VaultPath(dataRoot, profile, instance), profile, instance, passphrase)
	case secrets.BackendSecretService:
		return secrets.InitSecretService(ctx, dataRoot, profile, instance)
	case secrets.BackendKeychain:
		return secrets.InitKeychain(ctx, dataRoot, profile, instance)
	default:
		return nil, &secrets.BackendError{Kind: secrets.ErrBackendUnavailable, Backend: backend}
	}
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
	paths, err := s.paths()
	if err != nil {
		return purgeResult{}, err
	}
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
	err = secrets.WithProfileLock(ctx, paths.Data, profileName, func(ctx context.Context) error {
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

func (s *appState) purgeSecretService(ctx context.Context, instance, confirmation string) (purgeResult, error) {
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
	paths, err := s.paths()
	if err != nil {
		return purgeResult{}, err
	}
	result := purgeResult{
		Profile: profileName, Backend: secrets.BackendSecretService, Instance: instance,
		Path: "secret-service://" + profileName + "/" + instance,
	}
	if profile.Secrets != nil && profile.Secrets.Backend == string(secrets.BackendSecretService) && profile.Secrets.Instance == instance {
		result.Active = true
		return result, fmt.Errorf("refusing to purge active Secret Service instance %q", instance)
	}
	if err := secrets.InspectSecretService(ctx, paths.Data, profileName, instance); err != nil {
		return result, err
	}
	if confirmation == "" {
		return result, nil
	}
	if confirmation != instance {
		return result, fmt.Errorf("--confirm-instance must exactly match %q", instance)
	}
	err = secrets.WithProfileLock(ctx, paths.Data, profileName, func(ctx context.Context) error {
		latest, err := s.loadConfig()
		if err != nil {
			return err
		}
		_, latestProfile, err := latest.ResolveProfile(profileName)
		if err != nil {
			return err
		}
		if latestProfile.Secrets != nil && latestProfile.Secrets.Backend == string(secrets.BackendSecretService) && latestProfile.Secrets.Instance == instance {
			return fmt.Errorf("refusing to purge active Secret Service instance %q", instance)
		}
		_, err = secrets.PurgeSecretService(ctx, paths.Data, profileName, instance)
		return err
	})
	if err != nil {
		return result, err
	}
	result.Purged = true
	return result, nil
}

func (s *appState) purgeKeychain(ctx context.Context, instance, confirmation string) (purgeResult, error) {
	if err := s.requireOfficialKeychain(); err != nil {
		return purgeResult{}, err
	}
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
	paths, err := s.paths()
	if err != nil {
		return purgeResult{}, err
	}
	result := purgeResult{
		Profile: profileName, Backend: secrets.BackendKeychain, Instance: instance,
		Path: "keychain://" + profileName + "/" + instance,
	}
	if profile.Secrets != nil && profile.Secrets.Backend == string(secrets.BackendKeychain) && profile.Secrets.Instance == instance {
		result.Active = true
		return result, fmt.Errorf("refusing to purge active Keychain instance %q", instance)
	}
	if err := secrets.InspectKeychain(ctx, paths.Data, profileName, instance); err != nil {
		return result, err
	}
	if confirmation == "" {
		return result, nil
	}
	if confirmation != instance {
		return result, fmt.Errorf("--confirm-instance must exactly match %q", instance)
	}
	err = secrets.WithProfileLock(ctx, paths.Data, profileName, func(ctx context.Context) error {
		latest, err := s.loadConfig()
		if err != nil {
			return err
		}
		_, latestProfile, err := latest.ResolveProfile(profileName)
		if err != nil {
			return err
		}
		if latestProfile.Secrets != nil && latestProfile.Secrets.Backend == string(secrets.BackendKeychain) && latestProfile.Secrets.Instance == instance {
			return fmt.Errorf("refusing to purge active Keychain instance %q", instance)
		}
		_, err = secrets.PurgeKeychain(ctx, paths.Data, profileName, instance)
		return err
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

func (s *appState) readVaultPassphrase(ctx context.Context, confirm bool) ([]byte, error) {
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
	return readVaultPassphraseTTY(ctx, confirm)
}

func readVaultPassphraseTTY(ctx context.Context, confirm bool) ([]byte, error) {
	// #nosec G304 -- /dev/tty is the controlling terminal, never redirected stdin.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("vault passphrase source required: no controlling TTY: %w", err)
	}
	defer func() { _ = tty.Close() }()
	if !term.IsTerminal(int(tty.Fd())) {
		return nil, fmt.Errorf("vault passphrase source required: controlling TTY is unavailable")
	}
	if confirm {
		if _, err := fmt.Fprintln(tty, "warning: losing this passphrase permanently loses access to the vault"); err != nil {
			return nil, err
		}
	}
	first, err := readHiddenTTY(ctx, tty, "vault passphrase: ")
	if err != nil {
		return nil, err
	}
	if !confirm {
		return first, nil
	}
	second, err := readHiddenTTY(ctx, tty, "confirm vault passphrase: ")
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

func readHiddenTTY(ctx context.Context, tty *os.File, label string) ([]byte, error) {
	if _, err := fmt.Fprint(tty, label); err != nil {
		return nil, err
	}
	value, err := readPasswordContext(ctx, int(tty.Fd()))
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
