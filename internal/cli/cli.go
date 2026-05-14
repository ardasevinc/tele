package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/tele/internal/buildinfo"
	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/output"
	"github.com/ardasevinc/tele/internal/secrets"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	return e.err.Error()
}

func (e exitError) Unwrap() error {
	return e.err
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}

type appState struct {
	cfgPath string
	profile string
	json    bool
	jsonl   bool
	quiet   bool
	verbose bool

	in  io.Reader
	out io.Writer
	err io.Writer
}

func Execute(ctx context.Context, args []string) error {
	state := &appState{in: os.Stdin, out: os.Stdout, err: os.Stderr}
	cmd := rootCommand(ctx, state)
	cmd.SetArgs(args)
	cmd.SetIn(os.Stdin)
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	if err := cmd.ExecuteContext(ctx); err != nil {
		w := state.writer()
		if state.json || state.jsonl {
			_ = w.JSON(output.ErrorResponse{Error: output.ErrorBody{
				Code:    "command_failed",
				Message: err.Error(),
			}})
		} else {
			_, _ = fmt.Fprintln(state.err, "error:", err)
		}
		return exitError{code: 1, err: err}
	}
	return nil
}

func rootCommand(ctx context.Context, s *appState) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "tele",
		Short:         "Unofficial Telegram CLI for agents and humans",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       buildinfo.Version + " (" + buildinfo.Commit + ")",
	}
	cmd.PersistentFlags().StringVar(&s.cfgPath, "config", "", "config file path")
	cmd.PersistentFlags().StringVar(&s.profile, "profile", "", "profile name")
	cmd.PersistentFlags().BoolVar(&s.json, "json", false, "write JSON output")
	cmd.PersistentFlags().BoolVar(&s.jsonl, "jsonl", false, "write JSONL output")
	cmd.PersistentFlags().BoolVar(&s.quiet, "quiet", false, "suppress human info output")
	cmd.PersistentFlags().BoolVar(&s.verbose, "verbose", false, "write verbose diagnostics")
	cmd.AddCommand(authCommand(s), meCommand(s), chatsCommand(s), historyCommand(s), searchCommand(s), exportCommand(s), configCommand(s), profilesCommand(s), doctorCommand(s))
	cmd.AddCommand(&cobra.Command{
		Use:    "whoami",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return meCommand(s).RunE(cmd, args)
		},
	})
	_ = ctx
	return cmd
}

func authCommand(s *appState) *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Manage Telegram account auth"}
	var phone string
	var phoneEnv string
	var code string
	var codeEnv string
	var password string
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
				Code:           firstNonEmpty(code, envValue(codeEnv)),
				Password:       firstNonEmpty(password, envValue(passwordEnv)),
				NonInteractive: nonInteractive,
			}
			status, err := app.Login(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return writeValue(s, status, func(w output.Writer) error {
				if status.Authorized {
					return w.Print("authorized as " + accountLabel(status.Account))
				}
				return w.Print("not authorized")
			})
		},
	}
	login.Flags().StringVar(&phone, "phone", "", "phone number for login")
	login.Flags().StringVar(&phoneEnv, "phone-env", "", "environment variable containing phone number")
	login.Flags().StringVar(&code, "code", "", "login code")
	login.Flags().StringVar(&codeEnv, "code-env", "", "environment variable containing login code")
	login.Flags().StringVar(&password, "password", "", "2FA password")
	login.Flags().StringVar(&passwordEnv, "password-env", "", "environment variable containing 2FA password")
	login.Flags().BoolVar(&nonInteractive, "non-interactive", false, "fail instead of prompting for missing login values")
	cmd.AddCommand(login)
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
			return writeValue(s, status, func(w output.Writer) error {
				if status.Authorized {
					return w.Print("authorized as " + accountLabel(status.Account))
				}
				return w.Print("not authorized")
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Log out and delete local session material",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			if err := app.Logout(cmd.Context()); err != nil {
				return err
			}
			return writeValue(s, map[string]any{"logged_out": true}, func(w output.Writer) error {
				return w.Print("logged out")
			})
		},
	})
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
			return writeValue(s, status.Account, func(w output.Writer) error {
				return w.Print(accountLabel(status.Account))
			})
		},
	}
}

func chatsCommand(s *appState) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "chats",
		Aliases: []string{"dialogs"},
		Short:   "List accessible Telegram chats",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			chats, err := app.Chats(cmd.Context(), limit)
			if err != nil {
				return err
			}
			return writeValue(s, chats, func(w output.Writer) error {
				for _, chat := range chats {
					if _, err := fmt.Fprintf(w.Out, "%-22s %-10s %4d %s\n", chat.Ref, chat.Kind, chat.UnreadCount, displayChat(chat)); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum chats to return")
	return cmd
}

func historyCommand(s *appState) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history <peer>",
		Short: "Fetch recent messages from a peer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			w := s.writer()
			w.Warn("history reads may mark Telegram messages read")
			messages, err := app.History(cmd.Context(), args[0], limit)
			if err != nil {
				return err
			}
			return writeMessages(s, messages)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum messages to return")
	return cmd
}

func searchCommand(s *appState) *cobra.Command {
	var limit int
	var chat string
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search Telegram messages conservatively",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			w := s.writer()
			w.Warn("search reads may mark Telegram messages read")
			messages, err := app.Search(cmd.Context(), args[0], chat, limit)
			if err != nil {
				return err
			}
			return writeMessages(s, messages)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum messages to return")
	cmd.Flags().StringVar(&chat, "chat", "", "scope search to peer ref, username, or cached title")
	return cmd
}

func exportCommand(s *appState) *cobra.Command {
	var limit int
	var format string
	cmd := &cobra.Command{
		Use:   "export <peer>",
		Short: "Bounded export of recent messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "jsonl" && format != "markdown" {
				return fmt.Errorf("--format must be jsonl or markdown")
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			s.writer().Warn("export reads may mark Telegram messages read")
			messages, err := app.History(cmd.Context(), args[0], limit)
			if err != nil {
				return err
			}
			if format == "jsonl" {
				s.jsonl = true
				return writeMessages(s, messages)
			}
			for _, msg := range messages {
				if _, err := fmt.Fprintf(s.out, "- %s #%d %s\n", msg.Date, msg.ID, strings.TrimSpace(msg.Text)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum messages to export")
	cmd.Flags().StringVar(&format, "format", "jsonl", "export format: jsonl or markdown")
	return cmd
}

func configCommand(s *appState) *cobra.Command {
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
				return w.Print(path)
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
				return s.writer().JSON(cfg)
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
					return w.Print(cfg.DefaultProfile)
				})
			default:
				return fmt.Errorf("unknown config key %q", args[0])
			}
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> [value]",
		Short: "Set config value",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			profileName, profile, err := cfg.ResolveProfile(s.profile)
			if err != nil {
				return err
			}
			_, _ = cfg.EnsureProfile(profileName)
			switch args[0] {
			case "api-id":
				if len(args) != 2 {
					return fmt.Errorf("api-id requires a value")
				}
				id, err := tgapp.ParseAPIID(args[1])
				if err != nil {
					return err
				}
				profile.APIID = id
				cfg.Profiles[profileName] = profile
			case "api-hash":
				hash := ""
				if len(args) == 2 {
					hash = args[1]
				} else {
					if _, err := fmt.Fprint(s.err, "api_hash: "); err != nil {
						return err
					}
					var line string
					if _, err := fmt.Fscanln(s.in, &line); err != nil {
						return err
					}
					hash = line
				}
				app := tgapp.App{Config: cfg, Profile: profileName, Paths: mustPaths(), Secrets: secrets.NewStore(), In: s.in, Out: s.out, Err: s.err}
				if err := app.SetAPIHash(cmd.Context(), hash); err != nil {
					return err
				}
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
			if err := s.saveConfig(cfg); err != nil {
				return err
			}
			return writeValue(s, map[string]any{"ok": true}, func(w output.Writer) error {
				return w.Print("ok")
			})
		},
	})
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
					if _, err := fmt.Fprintf(w.Out, "%s %s\n", marker, name); err != nil {
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
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			if _, err := cfg.EnsureProfile(args[0]); err != nil {
				return err
			}
			cfg.DefaultProfile = args[0]
			if err := s.saveConfig(cfg); err != nil {
				return err
			}
			return writeValue(s, map[string]string{"default_profile": args[0]}, func(w output.Writer) error {
				return w.Print(args[0])
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
				return w.Print(name)
			})
		},
	})
	return cmd
}

func doctorCommand(s *appState) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check local tele setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.DefaultPaths()
			if err != nil {
				return err
			}
			if s.cfgPath != "" {
				paths.Config = s.cfgPath
			}
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			profile, _, err := cfg.ResolveProfile(s.profile)
			if err != nil {
				return err
			}
			modeErr := config.CheckFileMode(paths.Config)
			body := map[string]any{
				"version":        buildinfo.Version,
				"profile":        profile,
				"config":         paths.Config,
				"data":           paths.Data,
				"config_mode_ok": modeErr == nil,
				"keychain":       "macOS Keychain",
			}
			if modeErr != nil {
				body["config_mode_error"] = modeErr.Error()
			}
			return writeValue(s, body, func(w output.Writer) error {
				if _, err := fmt.Fprintf(w.Out, "version: %s\nprofile: %s\nconfig: %s\ndata: %s\n", buildinfo.Version, profile, paths.Config, paths.Data); err != nil {
					return err
				}
				if modeErr != nil {
					w.Warn(modeErr.Error())
				}
				return nil
			})
		},
	}
}

func (s *appState) telegramApp() (tgapp.App, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return tgapp.App{}, err
	}
	profileName, _, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return tgapp.App{}, err
	}
	paths := mustPaths()
	if s.cfgPath != "" {
		paths.Config = s.cfgPath
	}
	return tgapp.App{
		Config:  cfg,
		Profile: profileName,
		Paths:   paths,
		Secrets: secrets.NewStore(),
		In:      s.in,
		Out:     s.out,
		Err:     s.err,
	}, nil
}

func (s *appState) writer() output.Writer {
	format := output.Human
	if s.json {
		format = output.JSON
	}
	if s.jsonl {
		format = output.JSONL
	}
	return output.Writer{Out: s.out, Err: s.err, Format: format, Quiet: s.quiet}
}

func (s *appState) loadConfig() (config.Config, error) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return cfg, err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	return cfg, nil
}

func (s *appState) saveConfig(cfg config.Config) error {
	return config.Save(s.cfgPath, cfg)
}

func (s *appState) defaultLimit(value int) int {
	if value > 0 {
		return value
	}
	cfg, err := s.loadConfig()
	if err == nil && cfg.DefaultLimit > 0 {
		return cfg.DefaultLimit
	}
	return 50
}

func writeValue(s *appState, value any, human func(output.Writer) error) error {
	w := s.writer()
	if w.Format == output.JSON || w.Format == output.JSONL {
		return w.JSON(value)
	}
	return human(w)
}

func writeMessages(s *appState, messages []tgapp.Message) error {
	w := s.writer()
	if w.Format == output.JSON {
		return w.JSON(map[string]any{
			"side_effects": []string{"may_mark_read"},
			"messages":     messages,
		})
	}
	if w.Format == output.JSONL {
		items := make([]any, 0, len(messages))
		for _, msg := range messages {
			items = append(items, msg)
		}
		return w.JSONL(items)
	}
	for _, msg := range messages {
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			text = "[" + firstNonEmpty(msg.Media, msg.Service, "empty") + "]"
		}
		if _, err := fmt.Fprintf(w.Out, "%s #%d %s\n", msg.Date, msg.ID, text); err != nil {
			return err
		}
	}
	return nil
}

func accountLabel(a *tgapp.Account) string {
	if a == nil {
		return "unknown"
	}
	name := strings.TrimSpace(a.FirstName + " " + a.LastName)
	if a.Username != "" {
		if name != "" {
			return name + " @" + a.Username
		}
		return "@" + a.Username
	}
	if name != "" {
		return name
	}
	return fmt.Sprintf("%d", a.ID)
}

func displayChat(chat tgapp.Chat) string {
	if chat.Username != "" {
		return chat.Title + " @" + chat.Username
	}
	return chat.Title
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func envValue(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

func mustPaths() config.Paths {
	paths, err := config.DefaultPaths()
	if err != nil {
		return config.Paths{}
	}
	return paths
}
