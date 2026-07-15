package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/output"
	"github.com/ardasevinc/tele/internal/secrets"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

func configCommand(s *appState) *cobra.Command {
	var valueEnv string
	cmd := &cobra.Command{Use: "config", Short: "Manage tele config"}
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print config path",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.DefaultPaths()
			if err != nil {
				return err
			}
			path := s.cfgPath
			if path == "" {
				path = paths.Config
			}
			return writeValue(s, map[string]string{"config": path}, func(w output.Writer) error {
				return w.Print(safeHuman(path))
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "get [key]",
		Short: "Print config",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				view := publicConfig(cfg)
				return writeValue(s, view, func(w output.Writer) error {
					return w.JSON(view)
				})
			}
			switch args[0] {
			case "api-id":
				_, p, err := cfg.ResolveProfile(s.profile)
				if err != nil {
					return err
				}
				return writeValue(s, map[string]int64{"api_id": p.APIID}, func(w output.Writer) error {
					return w.Print(p.APIID)
				})
			case "default-profile":
				return writeValue(s, map[string]string{"default_profile": cfg.DefaultProfile}, func(w output.Writer) error {
					return w.Print(safeHuman(cfg.DefaultProfile))
				})
			default:
				return fmt.Errorf("unknown config key %q", args[0])
			}
		},
	})
	set := &cobra.Command{
		Use:   "set <key> [value]",
		Short: "Set config value",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if valueEnv != "" && args[0] != "api-hash" {
				return fmt.Errorf("--value-env is only valid for api-hash")
			}
			if args[0] == "api-hash" {
				if len(args) != 1 {
					return fmt.Errorf("api-hash must not be passed inline; use the hidden prompt or --value-env")
				}
				hash := envValue(valueEnv)
				var err error
				if hash == "" {
					hash, err = readSecret(s.in, s.err, "api_hash: ")
					if err != nil {
						return err
					}
				}
				cfg, err := s.loadConfig()
				if err != nil {
					return err
				}
				profileName, _, err := cfg.ResolveProfile(s.profile)
				if err != nil {
					return err
				}
				app := tgapp.App{Config: cfg, Profile: profileName, Paths: mustPaths(), Secrets: secrets.NewStore(), In: s.in, Out: s.out, Err: s.err}
				if err := app.SetAPIHash(cmd.Context(), hash); err != nil {
					return err
				}
			} else if err := config.Update(cmd.Context(), s.cfgPath, func(cfg *config.Config) error {
				switch args[0] {
				case "api-id":
					if len(args) != 2 {
						return fmt.Errorf("api-id requires a value")
					}
					id, err := tgapp.ParseAPIID(args[1])
					if err != nil {
						return err
					}
					profileName, profile, err := cfg.ResolveProfile(s.profile)
					if err != nil {
						return err
					}
					if _, err := cfg.EnsureProfile(profileName); err != nil {
						return err
					}
					profile.APIID = id
					cfg.Profiles[profileName] = profile
				case "default-profile":
					if len(args) != 2 {
						return fmt.Errorf("default-profile requires a value")
					}
					if _, err := cfg.EnsureProfile(args[1]); err != nil {
						return err
					}
					cfg.DefaultProfile = args[1]
				default:
					return fmt.Errorf("unknown config key %q", args[0])
				}
				return nil
			}); err != nil {
				return err
			}
			return writeValue(s, map[string]any{"ok": true}, func(w output.Writer) error {
				return w.Print("ok")
			})
		},
	}
	set.Flags().StringVar(&valueEnv, "value-env", "", "environment variable containing the API hash")
	cmd.AddCommand(set)
	return cmd
}

func profilesCommand(s *appState) *cobra.Command {
	cmd := &cobra.Command{Use: "profiles", Short: "Manage local account profiles"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(cfg.Profiles))
			for name := range cfg.Profiles {
				names = append(names, name)
			}
			sort.Strings(names)
			return writeValue(s, names, func(w output.Writer) error {
				for _, name := range names {
					marker := " "
					if name == cfg.DefaultProfile {
						marker = "*"
					}
					if _, err := fmt.Fprintf(w.Out, "%s %s\n", marker, safeHuman(name)); err != nil {
						return err
					}
				}
				return nil
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "use <name>",
		Short: "Create or select the default profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.Update(cmd.Context(), s.cfgPath, func(cfg *config.Config) error {
				if _, err := cfg.EnsureProfile(args[0]); err != nil {
					return err
				}
				cfg.DefaultProfile = args[0]
				return nil
			}); err != nil {
				return err
			}
			return writeValue(s, map[string]string{"default_profile": args[0]}, func(w output.Writer) error {
				return w.Print(safeHuman(args[0]))
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "current",
		Short: "Print the active profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			name, _, err := cfg.ResolveProfile(s.profile)
			if err != nil {
				return err
			}
			return writeValue(s, map[string]string{"profile": name}, func(w output.Writer) error {
				return w.Print(safeHuman(name))
			})
		},
	})
	return cmd
}
