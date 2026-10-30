package transporter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/exterior/providers"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want outcome
	}{
		{"upstream timeout", providers.ErrUpstreamTimeout, outcomeFailover},
		{"deadline exceeded", context.DeadlineExceeded, outcomeFailover},
		{"500", providers.UpstreamStatusError{Status: 500}, outcomeFailover},
		{"502", providers.UpstreamStatusError{Status: 502}, outcomeFailover},
		{"401 auth", providers.UpstreamStatusError{Status: 401}, outcomeFailover},
		{"403 auth", providers.UpstreamStatusError{Status: 403}, outcomeFailover},
		{"408", providers.UpstreamStatusError{Status: 408}, outcomeFailover},
		{"409", providers.UpstreamStatusError{Status: 409}, outcomeFailover},
		{"425", providers.UpstreamStatusError{Status: 425}, outcomeFailover},
		{"429", providers.UpstreamStatusError{Status: 429}, outcomeFailover},
		{"400 request", providers.UpstreamStatusError{Status: 400}, outcomeNoFailover},
		{"404 request", providers.UpstreamStatusError{Status: 404}, outcomeNoFailover},
		{"422 request", providers.UpstreamStatusError{Status: 422}, outcomeNoFailover},
		{"unknown network error", errors.New("dial tcp: connection refused"), outcomeFailover},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.err); got != tc.want {
				t.Fatalf("classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestBackoffCooldownFor(t *testing.T) {
	tests := []struct {
		name  string
		pol   BackoffPolicy
		trips int
		want  time.Duration
	}{
		{"no base uses fixed cooldown", BackoffPolicy{BreakerCooldown: 90 * time.Second}, 3, 90 * time.Second},
		{"first trip is base", BackoffPolicy{Base: time.Second, Factor: 2}, 1, time.Second},
		{"escalates by factor uncapped", BackoffPolicy{Base: time.Second, Factor: 2}, 3, 4 * time.Second},
		{"factor <= 1 does not escalate", BackoffPolicy{Base: time.Second, Factor: 1}, 4, time.Second},
		{"caps at max during escalation", BackoffPolicy{Base: time.Second, Factor: 2, Max: 5 * time.Second}, 6, 5 * time.Second},
		{"caps base above max after loop", BackoffPolicy{Base: 10 * time.Second, Factor: 2, Max: 5 * time.Second}, 1, 5 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pol.cooldownFor(tc.trips); got != tc.want {
				t.Fatalf("cooldownFor(%d) = %v, want %v", tc.trips, got, tc.want)
			}
		})
	}
}

// TestTargetHealthCooldownEscalates checks that consecutive trips lengthen the
// cooldown.
func TestTargetHealthCooldownEscalates(t *testing.T) {
	h := &targetHealth{}
	b := BackoffPolicy{BreakerThreshold: 1, Base: time.Second, Factor: 2, Max: time.Hour}
	now := time.Unix(0, 0)

	// The first failure opens the breaker with a cooldown of Base (1s).
	h.recordFailure(now, b)
	if h.available(now.Add(900*time.Millisecond), b) {
		t.Fatal("within first cooldown must be unavailable")
	}
	if !h.available(now.Add(time.Second), b) {
		t.Fatal("after first cooldown a probe should be allowed")
	}
	// A failed probe reopens it with a cooldown of Base*Factor (2s).
	probeAt := now.Add(time.Second)
	h.recordFailure(probeAt, b)
	if h.available(probeAt.Add(time.Second), b) {
		t.Fatal("escalated cooldown must still be closed at 1s")
	}
	if !h.available(probeAt.Add(2*time.Second), b) {
		t.Fatal("after escalated 2s cooldown a probe should be allowed")
	}
}

func TestTargetHealthBreakerDisabled(t *testing.T) {
	h := &targetHealth{}
	b := BackoffPolicy{} // zero threshold disables the breaker
	now := time.Unix(0, 0)
	if !h.available(now, b) {
		t.Fatal("disabled breaker must always be available")
	}
	h.recordFailure(now, b) // no-op when disabled
	if !h.available(now, b) {
		t.Fatal("still available after failure when disabled")
	}
}

func TestTargetHealthLifecycle(t *testing.T) {
	h := &targetHealth{}
	b := BackoffPolicy{BreakerThreshold: 2, BreakerCooldown: time.Minute}
	now := time.Unix(0, 0)

	// Closed: available.
	if !h.available(now, b) {
		t.Fatal("closed should be available")
	}
	// One failure: still closed.
	h.recordFailure(now, b)
	if !h.available(now, b) {
		t.Fatal("below threshold should remain available")
	}
	// Second failure: opens.
	h.recordFailure(now, b)
	if h.available(now, b) {
		t.Fatal("open breaker before cooldown must be unavailable")
	}
	// Still within cooldown.
	if h.available(now.Add(30*time.Second), b) {
		t.Fatal("still within cooldown must be unavailable")
	}
	// After cooldown: half-open probe allowed once.
	after := now.Add(2 * time.Minute)
	if !h.available(after, b) {
		t.Fatal("after cooldown a probe should be allowed")
	}
	if h.available(after, b) {
		t.Fatal("second concurrent probe must be denied")
	}
	// Probe fails: reopens.
	h.recordFailure(after, b)
	if h.available(after, b) {
		t.Fatal("failed probe must reopen the breaker")
	}
	// After cooldown again, probe then success closes.
	later := after.Add(2 * time.Minute)
	if !h.available(later, b) {
		t.Fatal("probe should be allowed again")
	}
	h.recordSuccess()
	if !h.available(later, b) {
		t.Fatal("closed breaker should be available after success")
	}
}
