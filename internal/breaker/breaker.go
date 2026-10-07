// Package breaker is a minimal lock-free circuit breaker for best-effort
// remote backends (the Redis rate limiter). After Trip
// consecutive failures it opens for Cooldown, during which callers skip
// the backend entirely. When the cooldown ends exactly one caller is let
// through to probe; everyone else keeps skipping until that probe reports
// back, so a still-dead backend is not hit by every concurrent request at
// once.
package breaker

import (
	"sync/atomic"
	"time"
)

type Breaker struct {
	trip     int32
	cooldown time.Duration

	fails atomic.Int32
	// openUntil is 0 while closed; otherwise the unixnano time until which
	// the backend is skipped.
	openUntil atomic.Int64
}

// New returns a closed breaker that opens after trip consecutive failures.
func New(trip int, cooldown time.Duration) *Breaker {
	return &Breaker{trip: int32(trip), cooldown: cooldown}
}

// Allow reports whether the caller may use the backend now.
func (b *Breaker) Allow() bool {
	until := b.openUntil.Load()
	if until == 0 {
		return true
	}
	now := time.Now().UnixNano()
	if now < until {
		return false
	}
	// Cooldown over: the caller that wins the swap probes, and pushing the
	// deadline forward keeps the rest skipping until the probe reports.
	return b.openUntil.CompareAndSwap(until, now+int64(b.cooldown))
}

// Success records a working round trip and closes the breaker.
func (b *Breaker) Success() {
	if b.fails.Load() != 0 {
		b.fails.Store(0)
	}
	if b.openUntil.Load() != 0 {
		b.openUntil.Store(0)
	}
}

// Failure records a backend error and opens the breaker once enough pile up.
func (b *Breaker) Failure() {
	if b.fails.Add(1) >= b.trip {
		b.fails.Store(0)
		b.openUntil.Store(time.Now().Add(b.cooldown).UnixNano())
	}
}
