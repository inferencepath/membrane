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

// StoreManager holds the active Snapshot built from the state store. Current is
// a lock-free atomic load. Reload rebuilds from the store and swaps in only a
// valid candidate, keeping the last-good Snapshot otherwise. Reloads are
// triggered after a Console edit, on SIGHUP or on an interval.
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

// WithStoreReloadObserver registers fn to receive the result of every reload
// attempt, for telemetry.
func WithStoreReloadObserver(fn func(ReloadResult)) StoreManagerOption {
	return func(m *StoreManager) { m.onReload = fn }
}

// WithSecretResolver sets the resolver for secret:<name> references. Without
// it, those references fail closed on every build.
func WithSecretResolver(r SecretResolver) StoreManagerOption {
	return func(m *StoreManager) { m.secretResolve = r }
}

// NewStoreManager builds the initial Snapshot from the store. If the store does
// not hold a valid configuration yet, it returns an error; the daemon then stays
// not ready while the Console remains available for setup.
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

// Current returns the active Snapshot. It is never nil.
func (m *StoreManager) Current() *chat.Snapshot { return m.ptr.Load() }

// Reload rebuilds the Snapshot from the store and swaps it in. On failure the
// last-good Snapshot keeps serving and a secret-free reason is returned.
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

// Watch reloads on every receive from trigger until ctx is done or trigger is
// closed.
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
