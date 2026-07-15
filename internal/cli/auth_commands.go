package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/tele/internal/output"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

func authCommand(s *appState) *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Manage Telegram account auth"}
	var phone string
	var phoneEnv string
	var codeEnv string
	var passwordEnv string
	var nonInteractive bool
	login := &cobra.Command{
		Use:   "login",
		Short: "Log in with Telegram phone-code auth",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			opts := tgapp.LoginOptions{
				Phone:          firstNonEmpty(phone, envValue(phoneEnv)),
				Code:           envValue(codeEnv),
				Password:       envValue(passwordEnv),
				NonInteractive: nonInteractive,
			}
			status, err := app.Login(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, publicAuthStatus(status), metaFromStatus(s, status), func(w output.Writer) error {
				if status.Authorized {
					return w.Print("authorized as " + accountLabel(status.Account))
				}
				return w.Print("not authorized")
			})
		},
	}
	login.Flags().StringVar(&phone, "phone", "", "phone number for login")
	login.Flags().StringVar(&phoneEnv, "phone-env", "", "environment variable containing phone number")
	login.Flags().StringVar(&codeEnv, "code-env", "", "environment variable containing login code")
	login.Flags().StringVar(&passwordEnv, "password-env", "", "environment variable containing 2FA password")
	login.Flags().BoolVar(&nonInteractive, "non-interactive", false, "fail instead of prompting for missing login values")
	cmd.AddCommand(login)
	start := &cobra.Command{
		Use:   "start",
		Short: "Start phone-code auth and store pending code hash",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			phoneValue := firstNonEmpty(phone, envValue(phoneEnv))
			if phoneValue == "" {
				return fmt.Errorf("phone is required")
			}
			status, err := app.AuthStart(cmd.Context(), phoneValue)
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, publicAuthStart(status), metaFromStatus(s, tgapp.AuthStatus{Profile: s.profileName()}), func(w output.Writer) error {
				if status.AlreadyAuthorized {
					return w.Print("already authorized")
				}
				return w.Print("code sent")
			})
		},
	}
	start.Flags().StringVar(&phone, "phone", "", "phone number for login")
	start.Flags().StringVar(&phoneEnv, "phone-env", "", "environment variable containing phone number")
	cmd.AddCommand(start)
	complete := &cobra.Command{
		Use:   "complete",
		Short: "Complete pending phone-code auth",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			status, err := app.AuthComplete(cmd.Context(), envValue(codeEnv), envValue(passwordEnv))
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, publicAuthStatus(status), metaFromStatus(s, status), func(w output.Writer) error {
				if status.Authorized {
					return w.Print("authorized as " + accountLabel(status.Account))
				}
				return w.Print("not authorized")
			})
		},
	}
	complete.Flags().StringVar(&codeEnv, "code-env", "", "environment variable containing login code")
	complete.Flags().StringVar(&passwordEnv, "password-env", "", "environment variable containing 2FA password")
	cmd.AddCommand(complete)
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show auth status",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			status, err := app.Status(cmd.Context())
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, publicAuthStatus(status), metaFromStatus(s, status), func(w output.Writer) error {
				if status.Authorized {
					return w.Print("authorized as " + accountLabel(status.Account))
				}
				return w.Print("not authorized")
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Revoke the Telegram authorization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			if err := app.LogoutRemote(cmd.Context()); err != nil {
				return err
			}
			return writeValue(s, map[string]any{"logged_out": true, "local_auth_deleted": false}, func(w output.Writer) error {
				return w.Print("logged out; local auth material retained (use tele auth reset-local --yes to delete it)")
			})
		},
	})
	var confirmReset bool
	resetLocal := &cobra.Command{
		Use:   "reset-local",
		Short: "Delete local session and pending auth material",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirmReset {
				return fmt.Errorf("local auth reset requires --yes")
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			if err := app.ResetLocalAuth(cmd.Context()); err != nil {
				return err
			}
			return writeValue(s, map[string]any{"local_auth_deleted": true}, func(w output.Writer) error {
				return w.Print("local auth material deleted")
			})
		},
	}
	resetLocal.Flags().BoolVar(&confirmReset, "yes", false, "confirm deletion of local auth material")
	cmd.AddCommand(resetLocal)
	return cmd
}

func meCommand(s *appState) *cobra.Command {
	return &cobra.Command{
		Use:   "me",
		Short: "Show the authorized Telegram account",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			status, err := app.Status(cmd.Context())
			if err != nil {
				return err
			}
			if !status.Authorized {
				return fmt.Errorf("not authorized; run tele auth login")
			}
			return writeValueWithMeta(s, publicAccount(status.Account), metaFromStatus(s, status), func(w output.Writer) error {
				return w.Print(accountLabel(status.Account))
			})
		},
	}
}
