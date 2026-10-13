package app

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/server/chat"
	"github.com/inferencepath/membrane/internal/store"
)

// StoreManager owns the active serving Snapshot built from the embedded state
// store and swaps it atomically on reload. It mirrors Manager's fail-closed,
// last-good, lock-free-read contract — Current() is a hot-path atomic load;
// Reload() rebuilds from the store and swaps only a valid candidate in. Unlike
// the file Manager, a reload is triggered explicitly (after a console edit) or
// on SIGHUP/interval, not by file-change polling.
type StoreManager struct {
	st     *store.Store
	obs    transporter.Observer
	logger *slog.Logger

	ptr           atomic.Pointer[chat.Snapshot]
	mu            sync.Mutex
	lastHash      [sha256.Size]byte
	onReload      func(ReloadResult)
	secretResolve SecretResolver
}

// StoreManagerOption configures a StoreManager.
type StoreManagerOption func(*StoreManager)

// WithStoreReloadObserver registers a callback invoked with the result of every
// reload attempt (applied/rejected/unchanged), to drive reload telemetry.
func WithStoreReloadObserver(fn func(ReloadResult)) StoreManagerOption {
	return func(m *StoreManager) { m.onReload = fn }
}

// WithSecretResolver injects the sealed-store resolver used for `secret:<name>`
// credential references (secrets.Manager.Resolve). Without it, sealed references
// fail closed on every build/reload.
func WithSecretResolver(r SecretResolver) StoreManagerOption {
	return func(m *StoreManager) { m.secretResolve = r }
}

// NewStoreManager builds the initial Snapshot from the store (fail-closed at
// startup) and returns a Manager over it. If the store does not yet hold a valid
// minimum configuration, it returns an error and no Manager — the daemon then
// stays not-ready while the console remains available to complete setup.
func NewStoreManager(st *store.Store, obs transporter.Observer, logger *slog.Logger, opts ...StoreManagerOption) (*StoreManager, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(nopWriter{}, nil))
	}
	m := &StoreManager{st: st, obs: obs, logger: logger}
	for _, opt := range opts {
		opt(m)
	}
	snap, _, err := BuildSnapshotFromStoreWithSecrets(st, obs, m.secretResolve)
	if err != nil {
		return nil, err
	}
	m.ptr.Store(snap)
	if h, herr := st.ContentHash(); herr == nil {
		m.lastHash = h
	}
	logger.Info("managed config loaded from store")
	return m, nil
}

// Current returns the active Snapshot (lock-free atomic load, never nil after
// construction).
func (m *StoreManager) Current() *chat.Snapshot { return m.ptr.Load() }

// Reload rebuilds the Snapshot from the store and swaps it in atomically. On any
// failure the last-good Snapshot is retained and a secret-free reason returned —
// the egress path never drops to an invalid configuration.
func (m *StoreManager) Reload() ReloadResult {
	m.mu.Lock()
	defer m.mu.Unlock()

	if h, herr := m.st.ContentHash(); herr == nil && h == m.lastHash {
		return m.report(ReloadResult{Applied: false, Unchanged: true})
	}

	snap, _, err := BuildSnapshotFromStoreWithSecrets(m.st, m.obs, m.secretResolve)
	if err != nil {
		m.logger.Warn("store reload rejected; keeping last-good configuration", "error", err.Error())
		return m.report(ReloadResult{Applied: false, Reason: err.Error()})
	}
	m.ptr.Store(snap)
	if h, herr := m.st.ContentHash(); herr == nil {
		m.lastHash = h
	}
	m.logger.Info("managed configuration reloaded")
	return m.report(ReloadResult{Applied: true})
}

func (m *StoreManager) report(r ReloadResult) ReloadResult {
	if m.onReload != nil {
		m.onReload(r)
	}
	return r
}

// Watch reloads on every tick of trigger until ctx is done (mirrors Manager).
func (m *StoreManager) Watch(ctx context.Context, trigger <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-trigger:
			if !ok {
				return
			}
			_ = m.Reload()
		}
	}
}
