package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/secrets"
)

func internalCommand(s *appState) *cobra.Command {
	cmd := &cobra.Command{Use: "internal", Hidden: true}
	compatibility := &cobra.Command{
		Use:    "compatibility",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := s.checkCompatibility(); err != nil {
				return err
			}
			_, err := fmt.Fprintln(s.out, "tele-compatible-v1")
			return err
		},
	}
	officialBuild := &cobra.Command{
		Use:    "official-build",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := s.requireOfficialKeychain(); err != nil {
				return err
			}
			_, err := fmt.Fprintln(s.out, "tele-official-build-v1")
			return err
		},
	}
	cmd.AddCommand(compatibility, officialBuild)
	return cmd
}

func (s *appState) checkCompatibility() error {
	paths, err := s.paths()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(paths.Config) // #nosec G304 -- this is the selected Tele config path.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := config.Parse(data)
	if err != nil {
		return fmt.Errorf("parse current config: %w", err)
	}
	profileName, profile, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return err
	}
	if profile.Secrets != nil && profile.Secrets.Backend == string(secrets.BackendVault) {
		path := secrets.VaultPath(paths.Data, profileName, profile.Secrets.Instance)
		if err := secrets.InspectVaultInstance(path, profile.Secrets.Instance); err != nil {
			return fmt.Errorf("inspect current vault: %w", err)
		}
	}
	return nil
}
