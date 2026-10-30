// Package daemon assembles the Membrane daemon: the state store, the
// data-plane handlers and the optional Console. cmd/membrane calls Run; Options
// provides optional extension points.
package daemon

import (
	"context"
	"database/sql"
	"flag"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/inferencepath/membrane/internal/app"
	"github.com/inferencepath/membrane/internal/config"
	"github.com/inferencepath/membrane/internal/consoleaudit"
	"github.com/inferencepath/membrane/internal/consolecli"
	"github.com/inferencepath/membrane/internal/consolestore"
	"github.com/inferencepath/membrane/internal/exterior/keyring"
	"github.com/inferencepath/membrane/internal/interior/audit"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/logging"
	"github.com/inferencepath/membrane/internal/observability/metrics"
	"github.com/inferencepath/membrane/internal/secrets"
	"github.com/inferencepath/membrane/internal/server"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/server/console"
	"github.com/inferencepath/membrane/internal/server/embeddings"
	"github.com/inferencepath/membrane/internal/server/images"
	"github.com/inferencepath/membrane/internal/server/moderations"
	"github.com/inferencepath/membrane/internal/server/rerank"
	"github.com/inferencepath/membrane/internal/server/responses"
	"github.com/inferencepath/membrane/internal/server/speech"
	"github.com/inferencepath/membrane/internal/server/transcription"
	"github.com/inferencepath/membrane/internal/store"
)

// BuildInfo is the version metadata set at link time.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildTime string
}

// Options holds optional process-level extensions. The zero value runs the
// plain daemon.
type Options struct {
	// ChatOptions are appended to the chat handler's options.
	ChatOptions []chat.HandlerOption
	// Build is an optional extension hook called once, after the state store
	// is open and before the handlers and Console are wired. An error stops
	// the daemon.
	Build func(BuildDeps) (BuildResult, error)
}

// BuildDeps are the daemon dependencies passed to Options.Build.
type BuildDeps struct {
	// DB is the state store connection. An extension may keep its own tables
	// in it.
	DB *sql.DB
	// Logger is the daemon logger.
	Logger *slog.Logger
	// Metrics is the identity-free, Pathway-keyed metrics registry.
	Metrics *metrics.Registry
}

// BuildResult holds the extensions returned by Options.Build.
type BuildResult struct {
	// ChatOptions are appended to the chat handler options.
	ChatOptions []chat.HandlerOption
	// Panels adds optional Console extension panels.
	Panels console.PanelProvider
}

// Run starts the daemon, or the config CLI when args[0] is "config", and
// returns the process exit code.
func Run(args []string, info BuildInfo, opts Options) int {
	if len(args) > 0 && args[0] == "config" {
		return consolecli.Run(args[1:], os.Stdout, os.Stderr)
	}

	fs := flag.NewFlagSet("membrane", flag.ContinueOnError)
	configPath := fs.String("config", "./config/membrane.yaml", "path to the Membrane config file")
	seedPath := fs.String("seed", "", "path to a declarative config bundle applied once when the store is empty (default: $MEMBRANE_SEED or <data_dir>/seed.yaml)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	logger := logging.New(slog.LevelInfo)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err.Error())
		return 1
	}

	srv := server.New(
		cfg.Server.Address,
		cfg.Server.ShutdownTimeout,
		server.VersionInfo{Version: info.Version, Commit: info.Commit, BuildTime: info.BuildTime},
		logger,
	)

	// Metrics are served even while the daemon is not ready.
	reg := metrics.NewRegistry()
	srv.Handle("/metrics", reg.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The state store under data_dir is the source of runtime config; the first
	// run creates an empty schema. An incomplete store keeps the data plane
	// failing closed until setup in the Console is finished, and the next
	// reload makes it ready without a restart.
	st, serr := store.Open(filepath.Join(cfg.DataDir, store.DBFileName))
	if serr != nil {
		logger.Error("failed to open control-plane state store", "error", serr.Error())
		return 1
	}
	defer func() { _ = st.Close() }()

	// Fail closed so an extension is never half-wired.
	var extPanels console.PanelProvider
	if opts.Build != nil {
		br, berr := opts.Build(BuildDeps{DB: st.DB(), Logger: logger, Metrics: reg})
		if berr != nil {
			logger.Error("extension build failed (fail-closed)", "error", berr.Error())
			return 1
		}
		opts.ChatOptions = append(opts.ChatOptions, br.ChatOptions...)
		extPanels = br.Panels
	}

	// A bad master key is not fatal: secret: credentials fail closed while the
	// Console stays up to fix it, and env:, file: and vault: credentials still
	// work.
	secretMgr := buildSecretManager(cfg.Secrets, st, logger)
	var secretResolve app.SecretResolver
	if secretMgr != nil {
		secretResolve = secretMgr.Resolve
	}

	// mgrPtr stays nil until the store holds a valid configuration, so the
	// daemon can become ready at runtime. Handlers fail closed while it is nil.
	var mgrPtr atomic.Pointer[app.StoreManager]
	audits := buildAuditSink(cfg.Audit, logger)

	buildMgr := func() app.ReloadResult {
		nm, berr := app.NewStoreManager(st, observerOf(reg), logger,
			app.WithStoreReloadObserver(reloadObserverOf(reg)),
			app.WithSecretResolver(secretResolve))
		if berr != nil {
			srv.SetNotReady("managed configuration incomplete")
			return app.ReloadResult{Reason: berr.Error()}
		}
		mgrPtr.Store(nm)
		srv.SetReady()
		return app.ReloadResult{Applied: true}
	}
	reloadFn := func() app.ReloadResult {
		if m := mgrPtr.Load(); m != nil {
			return m.Reload()
		}
		return buildMgr()
	}

	// adapter backs Console edits and declarative export and apply. Bundles go
	// through the same validation as the serving config, including secret:
	// resolution.
	adapter := consolestore.New(st, cfg, func() consolestore.ReloadOutcome {
		r := reloadFn()
		return consolestore.ReloadOutcome{Applied: r.Applied, Unchanged: r.Unchanged, Reason: r.Reason}
	}, consolestore.WithValidator(func(permBytes, callerBytes, credBytes, provBytes, capsBytes []byte) error {
		return app.ValidateSpecsWith(secretResolve, permBytes, callerBytes, credBytes, provBytes, capsBytes)
	}))

	seedIfEmpty(st, adapter, resolveSeedPath(*seedPath, cfg.DataDir), logger)

	if r := buildMgr(); !r.Applied {
		logger.Warn("managed configuration incomplete; failing closed (not ready)", "reason", r.Reason)
	} else {
		logger.Info("chat pathway enabled (managed store + hot reload)", "store", st.Path())
	}

	current := func() *chat.Snapshot {
		if m := mgrPtr.Load(); m != nil {
			return m.Current()
		}
		return nil
	}

	chatOpts := append([]chat.HandlerOption{chat.WithAudit(audits)}, opts.ChatOptions...)
	srv.Handle(chat.Route, chat.NewReloadable(current, logger, reg, chatOpts...))
	// Every handler shares the same Snapshot and serves only Pathways of its
	// own operation.
	srv.Handle(embeddings.Route, embeddings.NewReloadable(current, logger, reg, embeddings.WithAudit(audits)))
	srv.Handle(rerank.Route, rerank.NewReloadable(current, logger, reg, rerank.WithAudit(audits)))
	srv.Handle(responses.Route, responses.NewReloadable(current, logger, reg, responses.WithAudit(audits)))
	srv.Handle(images.Route, images.NewReloadable(current, logger, reg, images.WithAudit(audits)))
	srv.Handle(moderations.Route, moderations.NewReloadable(current, logger, reg, moderations.WithAudit(audits)))
	srv.Handle(transcription.Route, transcription.NewReloadable(current, logger, reg, transcription.WithAudit(audits)))
	srv.Handle(speech.Route, speech.NewReloadable(current, logger, reg, speech.WithAudit(audits)))
	watchReload(ctx, reloaderFunc(reloadFn), logger)

	// The Console runs on its own admin listener, off the egress path. If it
	// fails to start, only the Console is disabled.
	if cfg.Console.Enabled {
		cd := consoleDeps{
			cfgSrc:         adapter,
			editor:         adapter,
			bundle:         adapter,
			pathwayCreator: adapter,
			operations:     adapter,
			callers:        adapter,
			audit:          consoleAuditSource(cfg.Audit),
			chatBaseURL:    chatBaseURL(cfg.Server.Address),
			panels:         extPanels,
		}
		// Secret and provider management need the sealed secret store.
		if secretMgr != nil {
			cd.secrets = consolestore.NewSecretAdapter(st, secretMgr)
			cd.providers = consolestore.NewProviderAdapter(st, secretMgr)
		}
		if err := startConsole(ctx, cfg.Console, srv, reg, cd, info, logger); err != nil {
			logger.Error("membrane console disabled: failed to start", "error", err.Error())
		}
	}

	if err := srv.Run(ctx); err != nil {
		logger.Error("server error", "error", err.Error())
		return 1
	}
	return 0
}

// consoleDeps groups the dependencies passed to the Console. Optional ones are
// nil when unavailable.
type consoleDeps struct {
	cfgSrc         console.ConfigSource
	editor         console.ConfigEditor
	bundle         console.ConfigBundle
	pathwayCreator console.PathwayCreator
	operations     console.OperationCatalog
	callers        console.CallerManager
	audit          console.AuditSource
	secrets        console.SecretManager
	providers      console.ProviderManager
	panels         console.PanelProvider
	chatBaseURL    string
}

// resolveSeedPath returns the seed bundle path: the --seed flag, then
// $MEMBRANE_SEED, then <data_dir>/seed.yaml.
func resolveSeedPath(flagVal, dataDir string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("MEMBRANE_SEED"); env != "" {
		return env
	}
	return filepath.Join(dataDir, "seed.yaml")
}

// seedIfEmpty applies the seed bundle at path if the store holds no
// configuration yet. A missing file is not an error. An invalid bundle is
// logged and the daemon stays not ready.
func seedIfEmpty(st *store.Store, bundle console.ConfigBundle, path string, logger *slog.Logger) {
	configured, err := st.IsConfigured()
	if err != nil {
		logger.Warn("seed check failed; skipping seed", "error", err.Error())
		return
	}
	if configured {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("seed file unreadable; starting empty", "path", path, "error", err.Error())
		}
		return
	}
	res := bundle.ApplyBundle(raw, "")
	switch {
	case res.Applied:
		logger.Info("seeded managed configuration from bundle", "path", path)
	case res.Unchanged:
		logger.Info("seed bundle produced no changes", "path", path)
	default:
		logger.Warn("seed bundle rejected; starting empty (fail-closed)", "path", path, "reason", res.Reason)
	}
}

// consoleAuditSource returns an AuditSource over the audit file, or nil when
// audit records do not go to a file.
func consoleAuditSource(cfg config.AuditConfig) console.AuditSource {
	if cfg.Sink != "jsonlines" {
		return nil
	}
	switch cfg.Path {
	case "", "stdout", "stderr":
		return nil
	default:
		return consoleaudit.New(cfg.Path)
	}
}

// chatBaseURL returns the data-plane base URL for the Console's Test Pathway
// proxy. An empty or wildcard host becomes loopback.
func chatBaseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// startConsole starts the Console on its own listener until ctx is canceled.
// The egress mux is never mounted there. An admin token is needed only for
// non-loopback access.
func startConsole(ctx context.Context, cc config.ConsoleConfig, status console.StatusProvider, reg *metrics.Registry, deps consoleDeps, info BuildInfo, logger *slog.Logger) error {
	var token string
	if cc.AuthTokenRef != "" {
		v, err := console.ResolveTokenRef(cc.AuthTokenRef)
		if err != nil {
			return err
		}
		token = v
	}

	c := console.New(console.Config{
		Status:         status,
		Metrics:        reg,
		Config:         deps.cfgSrc,
		Editor:         deps.editor,
		Bundle:         deps.bundle,
		PathwayCreator: deps.pathwayCreator,
		Operations:     deps.operations,
		Callers:        deps.callers,
		Audit:          deps.audit,
		Secrets:        deps.secrets,
		Providers:      deps.providers,
		Panels:         deps.panels,
		ChatBaseURL:    deps.chatBaseURL,
		Version:        console.Version{Version: info.Version, Commit: info.Commit, BuildTime: info.BuildTime},
		AuthToken:      token,
		Logger:         logger,
	})

	consoleSrv := server.New(
		cc.Address,
		10*time.Second,
		server.VersionInfo{Version: info.Version, Commit: info.Commit, BuildTime: info.BuildTime},
		logger,
	)
	consoleSrv.Handle("/", c.Handler())
	consoleSrv.SetReady()

	go func() {
		if err := consoleSrv.Run(ctx); err != nil {
			logger.Error("membrane console server error", "error", err.Error())
		}
	}()
	logger.Info("membrane console enabled", "address", cc.Address, "auth", token != "")
	return nil
}

type reloader interface{ Reload() app.ReloadResult }

type reloaderFunc func() app.ReloadResult

func (f reloaderFunc) Reload() app.ReloadResult { return f() }

// watchReload reloads on each SIGHUP until ctx is done. A rejected reload keeps
// the last-good configuration.
func watchReload(ctx context.Context, mgr reloader, logger *slog.Logger) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		defer signal.Stop(hup)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if r := mgr.Reload(); !r.Applied && !r.Unchanged {
					logger.Warn("SIGHUP reload rejected; keeping last-good", "reason", r.Reason)
				}
			}
		}
	}()
}

// buildAuditSink returns the Interior-only audit Sink. A jsonlines sink writes
// to stdout, stderr or an append-only file. Audit is best-effort, so a file
// that cannot be opened falls back to Discard, which is also the default.
func buildAuditSink(cfg config.AuditConfig, logger *slog.Logger) audit.Sink {
	if cfg.Sink != "jsonlines" {
		return audit.Discard{}
	}
	var w io.Writer
	switch cfg.Path {
	case "", "stdout":
		w = os.Stdout
	case "stderr":
		w = os.Stderr
	default:
		f, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			logger.Warn("audit sink disabled: cannot open file", "error", err.Error())
			return audit.Discard{}
		}
		w = f
	}
	logger.Info("audit enabled", "sink", "jsonlines", "path", cfg.Path)
	return audit.NewJSONLines(w, func(err error) {
		logger.Warn("audit write failed", "error", err.Error())
	})
}

// buildSecretManager returns the sealed secret Manager for secret:<name>
// references. It returns nil, so those references fail closed, when no master
// key is configured or the key or Keeper cannot be set up; the latter is logged
// without the secret. The key version is fixed at 1.
func buildSecretManager(cfg config.SecretsConfig, st *store.Store, logger *slog.Logger) *secrets.Manager {
	if cfg.MasterKeyRef == "" {
		return nil
	}
	master, err := keyring.ResolveRef(cfg.MasterKeyRef)
	if err != nil {
		logger.Warn("sealed secret store disabled: cannot resolve master key; secret: references will fail closed", "error", err.Error())
		return nil
	}
	keeper, err := secrets.NewKeeper([]byte(master), 1)
	if err != nil {
		logger.Warn("sealed secret store disabled: cannot build keeper; secret: references will fail closed", "error", err.Error())
		return nil
	}
	logger.Info("sealed secret store enabled")
	return secrets.NewManager(st, keeper)
}

// observerOf returns rec as a transporter.Observer, or nil if it is not one.
func observerOf(rec metrics.Recorder) transporter.Observer {
	if o, ok := rec.(transporter.Observer); ok {
		return o
	}
	return nil
}

// reloadObserverOf returns a callback that records reload outcomes to rec, or a
// no-op if rec does not support reload telemetry.
func reloadObserverOf(rec metrics.Recorder) func(app.ReloadResult) {
	ro, ok := rec.(metrics.ReloadObserver)
	if !ok {
		return func(app.ReloadResult) {}
	}
	return func(r app.ReloadResult) {
		switch {
		case r.Applied:
			ro.ObserveReload("applied")
		case r.Unchanged:
			ro.ObserveReload("unchanged")
		default:
			ro.ObserveReload("rejected")
		}
	}
}
