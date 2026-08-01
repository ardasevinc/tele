package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ardasevinc/tele/internal/botapi"
	"github.com/ardasevinc/tele/internal/botfactory"
	"github.com/ardasevinc/tele/internal/botstore"
	"github.com/ardasevinc/tele/internal/buildinfo"
	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/output"
	"github.com/ardasevinc/tele/internal/secrets"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
	"github.com/ardasevinc/tele/internal/updater"
)

type exitError struct {
	code int
	err  error
}

type reportedError struct {
	code int
	err  error
}

func (e reportedError) Error() string { return e.err.Error() }
func (e reportedError) Unwrap() error { return e.err }

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
	cfgPath  string
	profile  string
	json     bool
	jsonl    bool
	quiet    bool
	readOnly bool
	dryRun   bool
	command  string
	wait     time.Duration
	timeout  time.Duration
	cancel   context.CancelFunc

	secretStore             secrets.Store
	secretBackend           string
	secretBackendSupported  bool
	secretBackendConfigured bool
	vaultPassphraseFD       int
	vaultPassphraseFile     string
	secretLazy              secrets.Store
	managerAPI              botapi.ManagerAPI
	managedTokenAPI         botapi.ManagedTokenAPI
	managedBotCreator       botfactory.ManagedBotCreator
	ownedBotDiscoverer      botfactory.OwnedBotDiscoverer
	botInventory            *botstore.Store
	pathOverride            *config.Paths
	updateClient            *updater.Client

	in  io.Reader
	out io.Writer
	err io.Writer
}

func Execute(ctx context.Context, args []string) error {
	state := &appState{in: os.Stdin, out: os.Stdout, err: os.Stderr, command: "tele"}
	return executeWithState(ctx, args, state)
}

func executeWithState(ctx context.Context, args []string, state *appState) error {
	defer func() {
		if state.cancel != nil {
			state.cancel()
		}
	}()
	if state.command == "" {
		state.command = "tele"
	}
	cmd := rootCommand(ctx, state)
	cmd.SetArgs(args)
	cmd.SetIn(state.in)
	cmd.SetOut(state.out)
	cmd.SetErr(state.err)
	if err := cmd.ExecuteContext(ctx); err != nil {
		var reported reportedError
		if errors.As(err, &reported) {
			return exitError(reported)
		}
		w := state.writer()
		response := output.ErrorFrom(err)
		meta := state.meta(0, "", nil)
		response.Meta = &meta
		if state.jsonl {
			_ = w.JSON(output.ErrorRecord(response))
		} else if state.json {
			_ = w.JSON(response)
		} else {
			_, _ = fmt.Fprintln(state.err, "error:", safeHuman(err.Error()))
		}
		return exitError{code: response.Error.ExitCode, err: err}
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
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			s.command = cmd.CommandPath()
			if s.json && s.jsonl {
				return fmt.Errorf("--json and --jsonl are mutually exclusive")
			}
			if err := tgapp.ValidateFloodWaitLimit(s.wait); err != nil {
				return err
			}
			return applyCommandTimeout(cmd, s)
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			if s.cancel != nil {
				s.cancel()
				s.cancel = nil
			}
		},
	}
	cmd.PersistentFlags().StringVar(&s.cfgPath, "config", "", "config file path")
	cmd.PersistentFlags().StringVar(&s.profile, "profile", "", "profile name")
	cmd.PersistentFlags().BoolVar(&s.json, "json", false, "write JSON output")
	cmd.PersistentFlags().BoolVar(&s.jsonl, "jsonl", false, "write JSONL output")
	cmd.PersistentFlags().BoolVar(&s.quiet, "quiet", false, "suppress human info output")
	cmd.PersistentFlags().BoolVar(&s.readOnly, "read-only", false, "reject Telegram mutations")
	cmd.PersistentFlags().BoolVar(&s.dryRun, "dry-run", false, "resolve and validate supported mutations without dispatching them")
	cmd.PersistentFlags().DurationVar(&s.wait, "wait", 0, "wait and retry flood limits within this total budget (max 5m)")
	cmd.PersistentFlags().Lookup("wait").NoOptDefVal = tgapp.DefaultFloodWaitLimit.String()
	cmd.PersistentFlags().DurationVar(&s.timeout, "timeout", 0, "total command timeout (0 selects a command-appropriate default; max 30m)")
	cmd.PersistentFlags().IntVar(&s.vaultPassphraseFD, "vault-passphrase-fd", -1, "read the vault passphrase from file descriptor N (N >= 3)")
	cmd.PersistentFlags().StringVar(&s.vaultPassphraseFile, "vault-passphrase-file", "", "read the vault passphrase from a protected file")
	commands := []*cobra.Command{authCommand(s), meCommand(s), chatsCommand(s), readCommand(s), searchCommand(s), exportCommand(s), inboxCommand(s), mediaCommand(s)}
	commands = append(commands, mutationCommands(s)...)
	cmd.AddCommand(commands...)
	cmd.AddCommand(configCommand(s), profilesCommand(s), doctorCommand(s))
	cmd.AddCommand(secretsCommand(s))
	cmd.AddCommand(botsCommand(s))
	cmd.AddCommand(updateCommand(s))
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

func (s *appState) telegramApp() (tgapp.App, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return tgapp.App{}, err
	}
	profileName, _, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return tgapp.App{}, err
	}
	paths, err := s.paths()
	if err != nil {
		return tgapp.App{}, err
	}
	return tgapp.App{
		Config:         cfg,
		Profile:        profileName,
		Paths:          paths,
		Secrets:        s.secrets(),
		FloodWaitLimit: s.wait,
		In:             s.in,
		Out:            s.out,
		Err:            s.err,
	}, nil
}

func (s *appState) secrets() secrets.Store {
	if s.secretStore != nil {
		return s.secretStore
	}
	if s.secretLazy == nil {
		s.secretLazy = &secrets.LazyStore{Open: s.openSecretStore}
	}
	return s.secretLazy
}

func (s *appState) secretBackendInfo() (string, bool) {
	if s.secretBackendConfigured {
		return s.secretBackend, s.secretBackendSupported
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return "configuration unavailable", false
	}
	_, profile, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return "configuration unavailable", false
	}
	if profile.Secrets == nil {
		return secrets.Backend()
	}
	id := secrets.BackendID(profile.Secrets.Backend)
	return secrets.BackendDisplayName(id), id == secrets.BackendVault || id == secrets.BackendKeychain || id == secrets.BackendKeychainLegacy || id == secrets.BackendSecretService
}

func (s *appState) secretBackendSelection() (secrets.BackendID, string) {
	cfg, err := s.loadConfig()
	if err != nil {
		return "", ""
	}
	_, profile, err := cfg.ResolveProfile(s.profile)
	if err != nil || profile.Secrets == nil {
		return "", ""
	}
	return secrets.BackendID(profile.Secrets.Backend), profile.Secrets.Instance
}

func (s *appState) vaultUnlockSource() string {
	if s.vaultPassphraseFD >= 3 {
		return "fd"
	}
	if s.vaultPassphraseFile != "" {
		return "file"
	}
	if s.json || s.jsonl {
		return "none"
	}
	return "tty"
}

func (s *appState) openSecretStore(ctx context.Context) (secrets.Store, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	profileName, profile, err := cfg.ResolveProfile(s.profile)
	if err != nil {
		return nil, err
	}
	selection := secrets.Selection{}
	if profile.Secrets != nil {
		selection.Backend = secrets.BackendID(profile.Secrets.Backend)
		selection.Instance = profile.Secrets.Instance
	}
	var passphrase []byte
	if selection.Backend == secrets.BackendVault {
		passphrase, err = s.readVaultPassphrase(ctx, false)
		if err != nil {
			return nil, err
		}
		defer zeroSecret(passphrase)
	}
	paths, err := s.paths()
	if err != nil {
		return nil, err
	}
	return secrets.Open(ctx, selection, secrets.OpenOptions{DataRoot: paths.Data, Profile: profileName, Passphrase: passphrase})
}

func (s *appState) botManagerAPI() botapi.ManagerAPI {
	if s.managerAPI != nil {
		return s.managerAPI
	}
	client := botapi.NewClient(nil)
	return client
}

func (s *appState) botTokenAPI() botapi.ManagedTokenAPI {
	if s.managedTokenAPI != nil {
		return s.managedTokenAPI
	}
	client := botapi.NewClient(nil)
	return client
}

func (s *appState) botCreator() (botfactory.ManagedBotCreator, error) {
	if s.managedBotCreator != nil {
		return s.managedBotCreator, nil
	}
	app, err := s.telegramApp()
	if err != nil {
		return nil, err
	}
	return app, nil
}

func (s *appState) botDiscoverer() (botfactory.OwnedBotDiscoverer, error) {
	if s.ownedBotDiscoverer != nil {
		return s.ownedBotDiscoverer, nil
	}
	app, err := s.telegramApp()
	if err != nil {
		return nil, err
	}
	return app, nil
}

func (s *appState) botsStore() (botstore.Store, error) {
	if s.botInventory != nil {
		return *s.botInventory, nil
	}
	paths, err := s.paths()
	if err != nil {
		return botstore.Store{}, err
	}
	return botstore.New(paths.Data, s.profileName()), nil
}

func (s *appState) withProfileSecretLock(ctx context.Context, fn func(context.Context) error) error {
	paths, err := s.paths()
	if err != nil {
		return err
	}
	return secrets.WithProfileLock(ctx, paths.Data, s.profileName(), fn)
}

func (s *appState) writer() output.Writer {
	return s.writerWithDefault(output.Human)
}

func (s *appState) writerWithDefault(defaultFormat output.Format) output.Writer {
	format := defaultFormat
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

func previewMutation(s *appState, ctx context.Context, action, peerRef string, msgID int, scope tgapp.DeleteScope) error {
	app, err := s.telegramApp()
	if err != nil {
		return err
	}
	preview, err := app.PreviewMutation(ctx, action, peerRef, msgID, scope)
	if err != nil {
		return err
	}
	meta := s.meta(0, preview.PeerRef, nil)
	return writeValueWithMeta(s, preview, meta, func(w output.Writer) error {
		return w.Print(fmt.Sprintf("[profile %s] dry-run: %s %s", safeHuman(meta.Profile), safeHuman(action), safeHuman(preview.PeerRef)))
	})
}

func (s *appState) meta(limit int, peerRef string, sideEffects []string) output.Meta {
	meta := output.NewMeta(s.profileName())
	meta.Command = s.command
	meta.TeleVersion = buildinfo.Version
	meta.Limit = limit
	meta.PeerRef = peerRef
	meta.SideEffects = sideEffects
	return meta
}

func applyRetrievalReceipt(meta *output.Meta, receipt tgapp.RetrievalReceipt) {
	meta.Retrieval = &output.RetrievalMeta{
		RequestedCount: receipt.RequestedCount,
		ReturnedCount:  receipt.ReturnedCount,
		Complete:       receipt.Complete,
		Truncated:      receipt.Truncated,
		NextCursor:     receipt.NextCursor,
		InputCursor:    receipt.InputCursor,
		ServerTotal:    receipt.ServerTotal,
		Pages:          receipt.Pages,
	}
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
	meta.Command = s.command
	meta.TeleVersion = buildinfo.Version
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

func (s *appState) requireWritable(action string) error {
	if !s.readOnly || s.dryRun {
		return nil
	}
	return fmt.Errorf("%s is disabled by --read-only", action)
}

func (s *appState) mutationReceipt(receipt string) string {
	return fmt.Sprintf("[profile %s] confirmed: %s", safeHuman(s.profileName()), safeHuman(receipt))
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
		if d <= 0 {
			return time.Time{}, fmt.Errorf("time duration %q must be positive", value)
		}
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
	if err := validateTextSources(text, textStdin); err != nil {
		return "", err
	}
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

func validateTextSources(text string, textStdin bool) error {
	if textStdin && text != "" {
		return fmt.Errorf("--text and --text-stdin are mutually exclusive")
	}
	return nil
}

const (
	defaultLocalTimeout    = 30 * time.Second
	defaultCommandTimeout  = 2 * time.Minute
	defaultAuthTimeout     = 5 * time.Minute
	defaultDownloadTimeout = 10 * time.Minute
	maxCommandTimeout      = 30 * time.Minute
)

func applyCommandTimeout(cmd *cobra.Command, s *appState) error {
	if s.timeout < 0 {
		return fmt.Errorf("--timeout must not be negative")
	}
	if s.timeout > maxCommandTimeout {
		return fmt.Errorf("--timeout must be at most %s", maxCommandTimeout)
	}
	duration := s.timeout
	if duration == 0 {
		duration = defaultTimeout(cmd.CommandPath())
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), duration)
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = cancel
	cmd.SetContext(ctx)
	return nil
}

func defaultTimeout(command string) time.Duration {
	switch command {
	case "tele auth login", "tele auth start", "tele auth complete":
		return defaultAuthTimeout
	case "tele media download":
		return defaultDownloadTimeout
	case "tele config get", "tele config path", "tele config set",
		"tele profiles current", "tele profiles list", "tele profiles use",
		"tele doctor", "tele auth reset-local", "tele secrets init", "tele secrets migrate", "tele secrets purge",
		"tele update":
		return defaultLocalTimeout
	default:
		return defaultCommandTimeout
	}
}

func parsePositiveInt(value, name string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

type publicConfigView struct {
	DefaultLimit   int                          `json:"default_limit"`
	DefaultProfile string                       `json:"default_profile"`
	Profiles       map[string]publicProfileView `json:"profiles"`
}

type publicProfileView struct {
	APIID int64 `json:"api_id,omitempty"`
}

func publicConfig(cfg config.Config) publicConfigView {
	view := publicConfigView{
		DefaultLimit:   cfg.DefaultLimit,
		DefaultProfile: cfg.DefaultProfile,
		Profiles:       make(map[string]publicProfileView, len(cfg.Profiles)),
	}
	for name, profile := range cfg.Profiles {
		view.Profiles[name] = publicProfileView{APIID: profile.APIID}
	}
	return view
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

func readSecret(in io.Reader, prompt io.Writer, label string) (string, error) {
	if _, err := fmt.Fprint(prompt, label); err != nil {
		return "", err
	}
	if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		value, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(prompt)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(value)), nil
	}
	var value string
	if _, err := fmt.Fscanln(in, &value); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func (s *appState) paths() (config.Paths, error) {
	paths, conflict, err := s.pathResolution()
	if err != nil {
		return config.Paths{}, err
	}
	if conflict != nil {
		return config.Paths{}, conflict
	}
	return paths, nil
}

func (s *appState) pathResolution() (config.Paths, *config.PathConflictError, error) {
	if s.pathOverride != nil {
		return *s.pathOverride, nil, nil
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		var conflict *config.PathConflictError
		if !errors.As(err, &conflict) {
			return config.Paths{}, nil, err
		}
		paths = conflict.Preferred
		remaining := make([]config.PathConflict, 0, len(conflict.Conflicts))
		for _, item := range conflict.Conflicts {
			if s.cfgPath == "" || item.Kind != "config" {
				remaining = append(remaining, item)
			}
		}
		if len(remaining) > 0 {
			return paths, &config.PathConflictError{Preferred: paths, Conflicts: remaining}, nil
		}
	}
	if s.cfgPath != "" {
		paths.Config = s.cfgPath
	}
	return paths, nil, nil
}
