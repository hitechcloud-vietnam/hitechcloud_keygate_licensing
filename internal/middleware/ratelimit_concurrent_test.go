package middleware

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// raceErrClient always errors, exercising the resilient backend's breaker
// atomics and in-memory fallback under concurrency.
type raceErrResult struct{}

func (raceErrResult) Int64() (int64, error) { return 0, errors.New("down") }

type raceErrClient struct{}

func (raceErrClient) Eval(context.Context, string, []string, ...any) RedisResult {
	return raceErrResult{}
}

func hammerBackend(b RateLimitBackend) {
	const workers = 16
	const iters = 300
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range iters {
				b.Allow("k"+strconv.Itoa((w+i)%8), 100, time.Minute)
			}
		}(w)
	}
	wg.Wait()
}

func TestMemoryBackendConcurrent(t *testing.T) { hammerBackend(NewMemoryBackend()) }

func TestResilientBackendConcurrent(t *testing.T) {
	hammerBackend(NewResilientRedisBackend(raceErrClient{}, "rl:"))
}
