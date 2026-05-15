package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

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
			_ = w.JSON(output.ErrorFrom(err))
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
	commands := []*cobra.Command{authCommand(s), meCommand(s), chatsCommand(s), readCommand(s), searchCommand(s), exportCommand(s), inboxCommand(s)}
	commands = append(commands, mutationCommands(s)...)
	cmd.AddCommand(commands...)
	cmd.AddCommand(configCommand(s), profilesCommand(s), doctorCommand(s))
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
			return writeValueWithMeta(s, status, metaFromStatus(s, status), func(w output.Writer) error {
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
			return writeValueWithMeta(s, status, metaFromStatus(s, tgapp.AuthStatus{Profile: s.profileName()}), func(w output.Writer) error {
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
			status, err := app.AuthComplete(cmd.Context(), firstNonEmpty(code, envValue(codeEnv)), firstNonEmpty(password, envValue(passwordEnv)))
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, status, metaFromStatus(s, status), func(w output.Writer) error {
				if status.Authorized {
					return w.Print("authorized as " + accountLabel(status.Account))
				}
				return w.Print("not authorized")
			})
		},
	}
	complete.Flags().StringVar(&code, "code", "", "login code")
	complete.Flags().StringVar(&codeEnv, "code-env", "", "environment variable containing login code")
	complete.Flags().StringVar(&password, "password", "", "2FA password")
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
			return writeValueWithMeta(s, status, metaFromStatus(s, status), func(w output.Writer) error {
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
			return writeValueWithMeta(s, status.Account, metaFromStatus(s, status), func(w output.Writer) error {
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
			return writeValueWithMeta(s, chats, s.telegramMeta(cmd.Context(), app, limit, "", nil), func(w output.Writer) error {
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

func readCommand(s *appState) *cobra.Command {
	var limit int
	var format string
	var since string
	var until string
	var afterID int
	var beforeID int
	var aroundID int
	var chronological bool
	cmd := &cobra.Command{
		Use:     "read <peer>",
		Aliases: []string{"history"},
		Short:   "Read bounded message history from a peer",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "human" && format != "transcript" {
				return fmt.Errorf("--format must be human or transcript")
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			opts := tgapp.ReadOptions{
				Peer:          args[0],
				Limit:         limit,
				AfterID:       afterID,
				BeforeID:      beforeID,
				AroundID:      aroundID,
				Chronological: chronological,
			}
			if since != "" {
				opts.Since, err = parseTimeFilter(since, time.Now())
				if err != nil {
					return err
				}
			}
			if until != "" {
				opts.Until, err = parseTimeFilter(until, time.Now())
				if err != nil {
					return err
				}
			}
			if format == "transcript" && !s.json && !s.jsonl {
				opts.Chronological = true
			}
			w := s.writer()
			w.Warn("read may mark Telegram messages read")
			messages, err := app.Read(cmd.Context(), opts)
			if err != nil {
				return err
			}
			meta := s.telegramMeta(cmd.Context(), app, limit, args[0], []string{"may_mark_read"})
			if format == "transcript" && !s.json && !s.jsonl {
				return writeTranscript(s, messages, meta, app.PeerInfo(args[0]))
			}
			return writeMessages(s, messages, meta)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum messages to return")
	cmd.Flags().StringVar(&format, "format", "human", "output format: human or transcript")
	cmd.Flags().StringVar(&since, "since", "", "only include messages since duration/date/RFC3339")
	cmd.Flags().StringVar(&until, "until", "", "only include messages until duration/date/RFC3339")
	cmd.Flags().IntVar(&afterID, "after-id", 0, "only include messages after this id")
	cmd.Flags().IntVar(&beforeID, "before-id", 0, "only include messages before this id")
	cmd.Flags().IntVar(&aroundID, "around", 0, "fetch context around this message id")
	cmd.Flags().BoolVar(&chronological, "chronological", false, "print oldest messages first")
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
			return writeMessages(s, messages, s.telegramMeta(cmd.Context(), app, limit, chat, []string{"may_mark_read"}))
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
			if format != "jsonl" && format != "markdown" && format != "transcript" {
				return fmt.Errorf("--format must be jsonl, markdown, or transcript")
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			if format == "jsonl" {
				s.jsonl = true
			}
			s.writer().Warn("export reads may mark Telegram messages read")
			messages, err := app.Read(cmd.Context(), tgapp.ReadOptions{Peer: args[0], Limit: limit, Chronological: format == "transcript"})
			if err != nil {
				return err
			}
			if format == "jsonl" {
				return writeMessages(s, messages, s.telegramMeta(cmd.Context(), app, limit, args[0], []string{"may_mark_read"}))
			}
			if format == "transcript" {
				return writeTranscript(s, messages, s.telegramMeta(cmd.Context(), app, limit, args[0], []string{"may_mark_read"}), app.PeerInfo(args[0]))
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
	cmd.Flags().StringVar(&format, "format", "jsonl", "export format: jsonl, markdown, or transcript")
	return cmd
}

func inboxCommand(s *appState) *cobra.Command {
	return inboxLikeCommand(s, "inbox", "", "List recent dialogs for triage")
}

func inboxLikeCommand(s *appState, name, mode, short string) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			limit = s.defaultLimit(limit)
			chats, err := app.Inbox(cmd.Context(), limit, mode)
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, chats, s.telegramMeta(cmd.Context(), app, limit, "", nil), func(w output.Writer) error {
				for _, chat := range chats {
					if _, err := fmt.Fprintf(w.Out, "%-22s unread=%-3d mentions=%-3d #%d %s %s\n", chat.Ref, chat.UnreadCount, chat.UnreadMentionsCount, chat.TopMessageID, displayChat(chat), strings.TrimSpace(chat.LastMessagePreview)); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum dialogs to return")
	if name == "inbox" {
		cmd.AddCommand(inboxLikeCommand(s, "unread", "unread", "List dialogs with unread messages"))
		cmd.AddCommand(inboxLikeCommand(s, "mentions", "mentions", "List dialogs with unread mentions"))
	}
	return cmd
}

func mutationCommands(s *appState) []*cobra.Command {
	return []*cobra.Command{
		sendCommand(s),
		replyCommand(s),
		reactCommand(s),
		editCommand(s),
		deleteCommand(s),
		inboxLikeCommand(s, "unread", "unread", "List dialogs with unread messages"),
		inboxLikeCommand(s, "mentions", "mentions", "List dialogs with unread mentions"),
	}
}

func sendCommand(s *appState) *cobra.Command {
	var text string
	var textStdin bool
	cmd := &cobra.Command{
		Use:   "send <peer>",
		Short: "Send a text message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := textInput(s, text, textStdin)
			if err != nil {
				return err
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.Send(cmd.Context(), args[0], body, 0)
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), func(w output.Writer) error {
				return w.Print(fmt.Sprintf("sent %s #%d", result.PeerRef, result.MessageID))
			})
		},
	}
	cmd.Flags().StringVar(&text, "text", "", "message text")
	cmd.Flags().BoolVar(&textStdin, "text-stdin", false, "read message text from stdin")
	return cmd
}

func replyCommand(s *appState) *cobra.Command {
	var text string
	var textStdin bool
	cmd := &cobra.Command{
		Use:   "reply <peer> <msg-id>",
		Short: "Reply to a message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, err := parsePositiveInt(args[1], "msg-id")
			if err != nil {
				return err
			}
			body, err := textInput(s, text, textStdin)
			if err != nil {
				return err
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.Send(cmd.Context(), args[0], body, msgID)
			if err != nil {
				return err
			}
			result.Action = "reply"
			return writeValueWithMeta(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), func(w output.Writer) error {
				return w.Print(fmt.Sprintf("replied %s #%d", result.PeerRef, result.MessageID))
			})
		},
	}
	cmd.Flags().StringVar(&text, "text", "", "message text")
	cmd.Flags().BoolVar(&textStdin, "text-stdin", false, "read message text from stdin")
	return cmd
}

func reactCommand(s *appState) *cobra.Command {
	var emoji string
	cmd := &cobra.Command{
		Use:   "react <peer> <msg-id>",
		Short: "React to a message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, err := parsePositiveInt(args[1], "msg-id")
			if err != nil {
				return err
			}
			if strings.TrimSpace(emoji) == "" {
				return fmt.Errorf("--emoji is required")
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.React(cmd.Context(), args[0], msgID, emoji)
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), func(w output.Writer) error {
				return w.Print(fmt.Sprintf("reacted %s #%d", result.PeerRef, result.MessageID))
			})
		},
	}
	cmd.Flags().StringVar(&emoji, "emoji", "", "reaction emoji")
	return cmd
}

func editCommand(s *appState) *cobra.Command {
	var text string
	var textStdin bool
	cmd := &cobra.Command{
		Use:   "edit <peer> <msg-id>",
		Short: "Edit one of your messages",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, err := parsePositiveInt(args[1], "msg-id")
			if err != nil {
				return err
			}
			body, err := textInput(s, text, textStdin)
			if err != nil {
				return err
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.Edit(cmd.Context(), args[0], msgID, body)
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), func(w output.Writer) error {
				return w.Print(fmt.Sprintf("edited %s #%d", result.PeerRef, result.MessageID))
			})
		},
	}
	cmd.Flags().StringVar(&text, "text", "", "new message text")
	cmd.Flags().BoolVar(&textStdin, "text-stdin", false, "read new message text from stdin")
	return cmd
}

func deleteCommand(s *appState) *cobra.Command {
	var forMe bool
	var revoke bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <peer> <msg-id>",
		Short: "Delete a message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("delete requires --yes")
			}
			if forMe == revoke {
				return fmt.Errorf("choose exactly one of --for-me or --revoke")
			}
			msgID, err := parsePositiveInt(args[1], "msg-id")
			if err != nil {
				return err
			}
			app, err := s.telegramApp()
			if err != nil {
				return err
			}
			result, err := app.DeleteMessage(cmd.Context(), args[0], msgID, revoke)
			if err != nil {
				return err
			}
			return writeValueWithMeta(s, result, s.telegramMeta(cmd.Context(), app, 0, result.PeerRef, nil), func(w output.Writer) error {
				return w.Print(fmt.Sprintf("deleted %s #%d", result.PeerRef, result.MessageID))
			})
		},
	}
	cmd.Flags().BoolVar(&forMe, "for-me", false, "delete only for the current account where Telegram supports it")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "delete for everyone where Telegram supports it")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm deletion")
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
	return writeValueWithMeta(s, value, s.meta(0, "", nil), human)
}

func writeValueWithMeta(s *appState, value any, meta output.Meta, human func(output.Writer) error) error {
	w := s.writer()
	if w.Format == output.JSON {
		return w.JSON(output.Envelope{Meta: meta, Data: value})
	}
	if w.Format == output.JSONL {
		return w.JSON(value)
	}
	return human(w)
}

func writeMessages(s *appState, messages []tgapp.Message, meta output.Meta) error {
	w := s.writer()
	if w.Format == output.JSON {
		return w.JSON(output.Envelope{Meta: meta, Data: messages})
	}
	if w.Format == output.JSONL {
		items := make([]any, 0, len(messages))
		for _, msg := range messages {
			items = append(items, output.Envelope{Meta: meta, Data: msg})
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

func writeTranscript(s *appState, messages []tgapp.Message, meta output.Meta, peer tgapp.PeerInfo) error {
	w := s.writer()
	if w.Format != output.Human {
		return writeMessages(s, messages, meta)
	}
	headerPeer := peer.Ref
	if headerPeer == "" {
		headerPeer = meta.PeerRef
	}
	label := peerLabel(peer)
	if label != "" {
		headerPeer += " (" + label + ")"
	}
	if _, err := fmt.Fprintf(w.Out, "peer: %s\n", headerPeer); err != nil {
		return err
	}
	if meta.FetchedAt != "" {
		if _, err := fmt.Fprintf(w.Out, "fetched_at: %s\n", meta.FetchedAt); err != nil {
			return err
		}
	}
	if meta.Limit > 0 {
		if _, err := fmt.Fprintf(w.Out, "limit: %d\n", meta.Limit); err != nil {
			return err
		}
	}
	if len(meta.SideEffects) > 0 {
		if _, err := fmt.Fprintf(w.Out, "side_effects: %s\n", strings.Join(meta.SideEffects, ", ")); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w.Out, "messages: %d\n\n", len(messages)); err != nil {
		return err
	}
	lastDay := ""
	for _, msg := range messages {
		day, clock := transcriptDateParts(msg.Date)
		if day != "" && day != lastDay {
			if lastDay != "" {
				if _, err := fmt.Fprintln(w.Out); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w.Out, "-- %s --\n", day); err != nil {
				return err
			}
			lastDay = day
		}
		if clock == "" {
			clock = "??:??"
		}
		speaker := "them"
		if msg.Outgoing {
			speaker = "me"
		}
		line := transcriptBody(msg)
		if _, err := fmt.Fprintf(w.Out, "[%d] %s %s: %s\n", msg.ID, clock, speaker, firstTranscriptLine(line)); err != nil {
			return err
		}
		for _, continuation := range transcriptContinuations(line) {
			if _, err := fmt.Fprintf(w.Out, "    %s\n", continuation); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *appState) meta(limit int, peerRef string, sideEffects []string) output.Meta {
	meta := output.NewMeta(s.profileName())
	meta.Limit = limit
	meta.PeerRef = peerRef
	meta.SideEffects = sideEffects
	return meta
}

func peerLabel(peer tgapp.PeerInfo) string {
	title := strings.TrimSpace(peer.Title)
	username := strings.TrimSpace(peer.Username)
	if username != "" {
		username = "@" + strings.TrimPrefix(username, "@")
	}
	switch {
	case title != "" && username != "":
		return title + " " + username
	case title != "":
		return title
	default:
		return username
	}
}

func transcriptDateParts(value string) (string, string) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", ""
	}
	t = t.UTC()
	return t.Format("2006-01-02"), t.Format("15:04")
}

func transcriptBody(msg tgapp.Message) string {
	text := strings.TrimSpace(msg.Text)
	if text != "" {
		return text
	}
	if msg.Media != "" {
		return "[" + mediaLabel(msg.Media) + "]"
	}
	if msg.Service != "" {
		return "[service: " + msg.Service + "]"
	}
	return "[empty]"
}

func mediaLabel(media string) string {
	label := strings.TrimPrefix(media, "messageMedia")
	if label == "" {
		return media
	}
	return strings.ToLower(label[:1]) + label[1:]
}

func firstTranscriptLine(value string) string {
	lines := transcriptLines(value)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func transcriptContinuations(value string) []string {
	lines := transcriptLines(value)
	if len(lines) <= 1 {
		return nil
	}
	return lines[1:]
}

func transcriptLines(value string) []string {
	raw := strings.Split(strings.TrimSpace(value), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, strings.TrimRight(line, " \t"))
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func (s *appState) telegramMeta(ctx context.Context, app tgapp.App, limit int, peerRef string, sideEffects []string) output.Meta {
	meta := s.meta(limit, peerRef, sideEffects)
	if !s.json && !s.jsonl {
		return meta
	}
	status, err := app.Status(ctx)
	if err == nil && status.Account != nil {
		meta.AccountID = status.Account.ID
	}
	return meta
}

func metaFromStatus(s *appState, status tgapp.AuthStatus) output.Meta {
	meta := output.NewMeta(firstNonEmpty(status.Profile, s.profileName()))
	if status.Account != nil {
		meta.AccountID = status.Account.ID
	}
	return meta
}

func (s *appState) profileName() string {
	cfg, err := s.loadConfig()
	if err != nil {
		return s.profile
	}
	name, _, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return s.profile
	}
	return name
}

func parseTimeFilter(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil || days <= 0 {
			return time.Time{}, fmt.Errorf("invalid day duration %q", value)
		}
		return now.Add(-time.Duration(days) * 24 * time.Hour), nil
	}
	if d, err := time.ParseDuration(value); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid time filter %q", value)
}

func textInput(s *appState, text string, textStdin bool) (string, error) {
	if textStdin {
		b, err := io.ReadAll(s.in)
		if err != nil {
			return "", err
		}
		text = string(b)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("message text is required; pass --text or --text-stdin")
	}
	return text, nil
}

func parsePositiveInt(value, name string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
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
