package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/middleware"
)

// redisNamespace returns the per-install key prefix (ending in ":") used
// to isolate this deployment's keys in a shared Redis logical DB. It is
// derived from BaseURL so two installs on different domains never collide;
// installs that share a domain should use separate Redis logical DBs
// (the /N suffix in REDIS_URL). Case and trailing slashes are normalized
// so replicas whose BASE_URL differs only cosmetically still share limits.
func redisNamespace(baseURL string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimRight(baseURL, "/"))))
	return "kg:" + hex.EncodeToString(sum[:])[:8] + ":"
}

// parseRedisURL accepts a redis:// or rediss:// URL, or a bare host:port
// (treated as redis://host:port). A parse error never echoes the URL back:
// net/url's error quotes the whole string, password included, and the
// caller logs it.
func parseRedisURL(raw string) (*redis.Options, error) {
	if !strings.Contains(raw, "://") {
		raw = "redis://" + raw
	}
	opt, err := redis.ParseURL(raw)
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return nil, uerr.Err
	}
	return opt, err
}

// rlAdapter adapts a *redis.Client to middleware.RedisClient. go-redis'
// Eval returns *redis.Cmd (which already has Int64), but a method
// returning the concrete type does not satisfy an interface method
// returning middleware.RedisResult, so this thin wrapper bridges them.
type rlAdapter struct{ c *redis.Client }

func (a rlAdapter) Eval(ctx context.Context, script string, keys []string, args ...any) middleware.RedisResult {
	return a.c.Eval(ctx, script, keys, args...)
}

// setupRedis connects to a Redis-compatible endpoint (Redis or Valkey —
// same protocol and client) when REDIS_URL is set, wires it as the
// shared, resilient rate-limit backend, and returns the client so the
// caller can close it on shutdown. namespace isolates this install's keys
// in a shared DB. Anything that goes wrong — no URL, a bad URL, an
// unreachable server — falls back to the in-memory limiter rather than
// failing startup: rate limiting is best-effort, and a single-node install
// with no Redis is a supported mode.
func setupRedis(url, namespace string, logger *slog.Logger) *redis.Client {
	if url == "" {
		logger.Info("rate limiting: in-memory backend (set REDIS_URL for a shared Redis-compatible backend)")
		return nil
	}
	opt, err := parseRedisURL(url)
	if err != nil {
		logger.Error("invalid REDIS_URL; falling back to in-memory backends", "error", err)
		return nil
	}
	// Tight timeouts and no long retry chain: a slow or down endpoint must
	// fail fast so requests degrade to the in-memory backends instead of
	// blocking for seconds on go-redis' generous defaults (Dial 5s, Read
	// 3s, 3 retries).
	opt.DialTimeout = 2 * time.Second
	opt.ReadTimeout = 300 * time.Millisecond
	opt.WriteTimeout = 300 * time.Millisecond
	opt.PoolTimeout = 500 * time.Millisecond
	// Honor the per-call context deadline the rate limiter (250ms) sets.
	// Without this go-redis ignores the caller's context
	// for network timeouts and uses DialTimeout/ReadTimeout instead, so a
	// slow connect could block for the full 2s dial before degrading.
	opt.ContextTimeoutEnabled = true
	// Disable client-side retries (-1, not 0 which means "default 3"). The
	// rate-limit Lua runs INCR, which is not idempotent: if the connection
	// drops after the server ran it but before the reply arrives, a retry
	// would count the request twice and trip 429 early. Resilience is
	// handled by our own timeout + breaker + in-memory fallback instead.
	opt.MaxRetries = -1
	client := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		// Do NOT give up permanently: the endpoint is often just briefly
		// unreachable during a rolling deploy. Keep the client and install
		// the resilient backend anyway: its per-call timeout and breaker
		// degrade to in-memory now and reconnect automatically once Redis
		// returns, without a restart. A permanent misconfig stays cheap
		// because the breaker stops hammering a dead endpoint.
		logger.Warn("Redis-compatible endpoint unreachable at startup; degrading to in-memory and retrying at runtime", "error", err)
	} else {
		logger.Info("rate limiting: Redis-compatible backend enabled", "namespace", namespace)
	}
	middleware.SetRateLimitBackend(middleware.NewResilientRedisBackend(rlAdapter{client}, namespace+"rl:"))
	return client
}
