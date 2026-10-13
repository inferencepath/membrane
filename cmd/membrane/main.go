// Command membrane is the Membrane entrypoint. It runs a single, managed
// operating model: the embedded state store under data_dir is the sole source of
// truth for runtime config, served through a fail-closed, last-good, hot-reloaded
// chat Snapshot and edited via the Membrane Console (off the egress path).
package main

import (
	"context"
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

// Build metadata, injected via -ldflags at build time.
var (
	version   = "0.0.0-dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// `membrane config ...` is the declarative-config CLI (a `deck`-equivalent
	// Admin-API client); everything else starts the daemon.
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
		server.VersionInfo{Version: version, Commit: commit, BuildTime: buildTime},
		logger,
	)

	// Identity-free, Pathway-keyed metrics. Exposed regardless of whether the
	// chat slice is wired, so operators always have a scrape target.
	reg := metrics.NewRegistry()
	srv.Handle("/metrics", reg.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Managed mode is the single operating model: the embedded state store under
	// data_dir is the sole source of truth for runtime config. First run creates
	// and migrates an empty-but-valid schema — the "just works from docker compose"
	// bootstrap. A fresh/incomplete store keeps the egress path fail-closed (not
	// ready) while the Console stays up to complete setup via the UI, then becomes
	// ready on the next reload (no restart). Opening the store is fatal only on
	// I/O error.
	st, serr := store.Open(filepath.Join(cfg.DataDir, store.DBFileName))
	if serr != nil {
		logger.Error("failed to open control-plane state store", "error", serr.Error())
		return 1
	}
	defer func() { _ = st.Close() }()

	// Sealed secret store: when a master key is configured, build the
	// Manager that seals/unseals `secret:<name>` credential references from the
	// store. A configured-but-unresolvable key is NOT fatal — it leaves the Manager
	// nil so sealed credentials fail closed (not-ready) while the Console stays up
	// to fix the key; credentials using only env:/file:/vault: are unaffected.
	secretMgr := buildSecretManager(cfg.Secrets, st, logger)
	var secretResolve app.SecretResolver
	if secretMgr != nil {
		secretResolve = secretMgr.Resolve
	}

	// The active StoreManager is held behind an atomic pointer so the daemon can
	// transition not-ready → ready at runtime once the operator completes setup.
	// The data-plane handler reads it per request (nil ⇒ fail closed).
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

	// The console/store adapter is the single read/edit + declarative
	// export/apply surface over the store. It injects app.ValidateSpecs so a
	// bundle is validated identically to the serving path (cross-kind keyring
	// resolution + per-target adapter build), never a weaker check.
	adapter := consolestore.New(st, cfg, func() consolestore.ReloadOutcome {
		r := reloadFn()
		return consolestore.ReloadOutcome{Applied: r.Applied, Unchanged: r.Unchanged, Reason: r.Reason}
	}, consolestore.WithValidator(func(permBytes, callerBytes, credBytes, provBytes, capsBytes []byte) error {
		// Validate declarative bundles identically to the serving path, including
		// sealed `secret:` resolution.
		return app.ValidateSpecsWith(secretResolve, permBytes, callerBytes, credBytes, provBytes, capsBytes)
	}))

	// Seed-on-empty: on a fresh store (no pathways yet), apply a declarative
	// seed bundle once so the daemon "just works from docker compose" with a
	// baseline config. Idempotent: skipped the moment the store is configured.
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
	srv.Handle(chat.Route, chat.NewReloadable(current, logger, reg, chat.WithAudit(audits)))
	// The /v1/embeddings handler reuses the same managed Snapshot and is gated
	// to Pathways whose operation is `embeddings`.
	srv.Handle(embeddings.Route, embeddings.NewReloadable(current, logger, reg, embeddings.WithAudit(audits)))
	// The /v1/rerank handler reuses the same managed Snapshot and is gated to
	// Pathways whose operation is `rerank`.
	srv.Handle(rerank.Route, rerank.NewReloadable(current, logger, reg, rerank.WithAudit(audits)))
	// The /v1/responses handler reuses the same managed Snapshot and is gated
	// to Pathways whose operation is `responses`.
	srv.Handle(responses.Route, responses.NewReloadable(current, logger, reg, responses.WithAudit(audits)))
	// The /v1/images/generations handler reuses the same managed Snapshot and is
	// gated to Pathways whose operation is `image_generation`.
	srv.Handle(images.Route, images.NewReloadable(current, logger, reg, images.WithAudit(audits)))
	// Moderations, audio transcription (multipart in), and audio speech (binary
	// out). Each reuses the same managed Snapshot and is gated to its own
	// operation.
	srv.Handle(moderations.Route, moderations.NewReloadable(current, logger, reg, moderations.WithAudit(audits)))
	srv.Handle(transcription.Route, transcription.NewReloadable(current, logger, reg, transcription.WithAudit(audits)))
	srv.Handle(speech.Route, speech.NewReloadable(current, logger, reg, speech.WithAudit(audits)))
	watchReload(ctx, reloaderFunc(reloadFn), logger)

	// Membrane Console: a built-in operator UI on a SEPARATE admin listener, off
	// the egress path. Disabled by default; a startup failure disables only the
	// console and never affects the data plane.
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
		}
		// Wire the sealed-secret + provider management ports only when the store
		// is enabled (a master key is configured).
		if secretMgr != nil {
			cd.secrets = consolestore.NewSecretAdapter(st, secretMgr)
			cd.providers = consolestore.NewProviderAdapter(st, secretMgr)
		}
		if err := startConsole(ctx, cfg.Console, srv, reg, cd, logger); err != nil {
			logger.Error("membrane console disabled: failed to start", "error", err.Error())
		}
	}

	if err := srv.Run(ctx); err != nil {
		logger.Error("server error", "error", err.Error())
		return 1
	}
	return 0
}

// consoleDeps groups the console's injected dependencies that vary with the
// daemon's mode (config source, optional editor, optional audit source, and the
// data-plane URL for the Test Pathway proxy).
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
	chatBaseURL    string
}

// resolveSeedPath resolves the seed bundle path: an explicit --seed flag wins,
// then $MEMBRANE_SEED, else the conventional <data_dir>/seed.yaml.
func resolveSeedPath(flagVal, dataDir string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("MEMBRANE_SEED"); env != "" {
		return env
	}
	return filepath.Join(dataDir, "seed.yaml")
}

// seedIfEmpty applies a declarative seed bundle exactly once, when the store
// holds no configuration yet. A missing seed file is not an error (the operator
// may configure via the Console instead); a present-but-invalid seed is logged
// and the daemon stays fail-closed (not ready) rather than serving a bad config.
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

// consoleAuditSource returns a console AuditSource reading the configured file
// audit sink, or nil when auditing writes to a stream/discard (no file to tail).
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

// chatBaseURL derives the data-plane base URL (for the Test Pathway proxy)
// from the server bind address. A wildcard/empty host is normalized to loopback.
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

// startConsole builds the Membrane Console and starts it on its own admin
// listener (reusing the server shell for graceful shutdown). It never mounts the
// egress mux. A resolved admin token (reference-only) is required only to permit
// non-local access; loopback access is always allowed. The listener runs until
// ctx is cancelled.
func startConsole(ctx context.Context, cc config.ConsoleConfig, status console.StatusProvider, reg *metrics.Registry, deps consoleDeps, logger *slog.Logger) error {
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
		ChatBaseURL:    deps.chatBaseURL,
		Version:        console.Version{Version: version, Commit: commit, BuildTime: buildTime},
		AuthToken:      token,
		Logger:         logger,
	})

	consoleSrv := server.New(
		cc.Address,
		10*time.Second,
		server.VersionInfo{Version: version, Commit: commit, BuildTime: buildTime},
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

// reloader is the minimal reload surface the SIGHUP watcher drives.
type reloader interface{ Reload() app.ReloadResult }

// reloaderFunc adapts a reload closure (managed mode's lazy-manager reload) to
// the reloader interface used by the SIGHUP watcher.
type reloaderFunc func() app.ReloadResult

func (f reloaderFunc) Reload() app.ReloadResult { return f() }

// watchReload reloads the configuration on each SIGHUP until ctx is done. A
// rejected reload keeps the last-good configuration and logs a secret-free reason.
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

// buildAuditSink constructs the Interior-only usage audit Sink from config. A
// jsonlines sink writes to stdout/stderr or an append-mode file; a file that
// cannot be opened logs a warning and falls back to Discard (audit is
// best-effort and never blocks serving). The default is Discard.
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

// buildSecretManager builds the sealed-secret Manager (seal/unseal + persist) for
// `secret:<name>` references from the configured master key. It returns nil
// (sealed references fail closed, Console secrets endpoints 503) when no master
// key is configured, or when the key cannot be resolved or a Keeper cannot be
// built — in which case it logs a secret-free warning and leaves the daemon up
// (Console available) rather than crashing. keyVersion is fixed at 1 until
// master-key rotation ships.
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

// observerOf returns rec as a transporter.Observer when it supports the
// resilience signals (the metrics Registry does); otherwise nil (no-op).
func observerOf(rec metrics.Recorder) transporter.Observer {
	if o, ok := rec.(transporter.Observer); ok {
		return o
	}
	return nil
}

// reloadObserverOf returns a reload-result callback that records the outcome to
// rec when it supports reload telemetry; otherwise a no-op callback.
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
