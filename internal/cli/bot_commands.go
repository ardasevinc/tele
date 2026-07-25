package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/tele/internal/botfactory"
	"github.com/ardasevinc/tele/internal/output"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

func botsCommand(s *appState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bots",
		Short: "Create and manage Telegram bots",
	}
	cmd.AddCommand(botManagerCommand(s))
	cmd.AddCommand(botUsernameCommand(s))
	cmd.AddCommand(botCreateCommand(s))
	return cmd
}

func botCreateCommand(s *appState) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "create <username>",
		Short: "Create a bot owned by the current user and managed by the configured manager",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.requireWritable("bots create"); err != nil {
				return err
			}
			if s.dryRun {
				return fmt.Errorf("--dry-run is not supported for bots create; use bots username check")
			}
			backend, supported := s.secretBackendInfo()
			if !supported {
				return fmt.Errorf("managed bot credentials require a supported secret store: %s", backend)
			}
			creator, err := s.botCreator()
			if err != nil {
				return err
			}
			inventory := s.botsStore()
			result, err := botfactory.Create(
				cmd.Context(),
				s.secrets(),
				s.botTokenAPI(),
				creator,
				inventory,
				s.profileName(),
				args[0],
				name,
				backend,
			)
			if err != nil {
				return err
			}
			err = writeValue(s, result, func(w output.Writer) error {
				return w.Print(fmt.Sprintf(
					"[profile %s] created @%s; token stored in %s",
					safeHuman(s.profileName()),
					safeHuman(result.Bot.Username),
					safeHuman(result.Token.SecretBackend),
				))
			})
			if err != nil {
				return tgapp.MutationError{
					Outcome:              tgapp.MutationConfirmed,
					RetrySafe:            false,
					ReconciliationHandle: result.ReconciliationHandle,
					Err:                  err,
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name for the new bot")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func botUsernameCommand(s *appState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "username",
		Short: "Check bot usernames",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "check <username>",
		Short: "Check whether a managed-bot username is available",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.CheckBotUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, result, s.telegramMeta(cmd.Context(), app, 0, "", nil), func(w output.Writer) error {
				state := "unavailable"
				if result.Available {
					state = "available"
				}
				return w.Print(fmt.Sprintf("@%s is %s", safeHuman(result.Username), state))
			})
		},
	})
	return cmd
}

func botManagerCommand(s *appState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manager",
		Short: "Configure the managed-bot control plane",
	}
	cmd.AddCommand(botManagerConfigureCommand(s), botManagerStatusCommand(s))
	return cmd
}

func botManagerConfigureCommand(s *appState) *cobra.Command {
	var tokenEnv string
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:   "configure <manager>",
		Short: "Verify and securely store a manager bot credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tokenEnv != "" && tokenStdin {
				return fmt.Errorf("--token-env and --token-stdin are mutually exclusive")
			}
			token := envValue(tokenEnv)
			var err error
			if tokenEnv != "" && token == "" {
				return fmt.Errorf("environment variable %s is empty", tokenEnv)
			}
			if token == "" {
				token, err = readSecret(s.in, s.err, "manager token: ")
				if err != nil {
					return err
				}
			}
			backend, supported := s.secretBackendInfo()
			if !supported {
				return fmt.Errorf("manager credential requires a supported secret store: %s", backend)
			}
			status, err := botfactory.ConfigureManager(
				cmd.Context(),
				s.secrets(),
				s.botManagerAPI(),
				s.profileName(),
				args[0],
				token,
				backend,
			)
			if err != nil {
				return err
			}
			return writeValue(s, status, func(w output.Writer) error {
				return w.Print(fmt.Sprintf(
					"[profile %s] manager @%s verified; token stored in %s",
					safeHuman(s.profileName()),
					safeHuman(status.Username),
					safeHuman(status.SecretBackend),
				))
			})
		},
	}
	cmd.Flags().StringVar(&tokenEnv, "token-env", "", "environment variable containing the manager token")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the manager token from stdin")
	return cmd
}

func botManagerStatusCommand(s *appState) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Verify the configured manager bot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			backend, supported := s.secretBackendInfo()
			if !supported {
				return fmt.Errorf("manager credential requires a supported secret store: %s", backend)
			}
			status, err := botfactory.VerifyManager(
				cmd.Context(),
				s.secrets(),
				s.botManagerAPI(),
				s.profileName(),
				backend,
			)
			if err != nil {
				return err
			}
			return writeValue(s, status, func(w output.Writer) error {
				if !status.Configured {
					return w.Print(fmt.Sprintf("[profile %s] no manager configured", safeHuman(s.profileName())))
				}
				return w.Print(fmt.Sprintf(
					"[profile %s] manager @%s verified; Bot Management Mode enabled",
					safeHuman(s.profileName()),
					safeHuman(status.Username),
				))
			})
		},
	}
}
