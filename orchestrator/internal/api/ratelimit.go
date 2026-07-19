package api

import (
	"sync"
	"time"
)

// rateLimiter is a per-key token bucket. It caps how many control-API requests a
// single API key may make, returning false when a request must be rejected (429).
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens refilled per second
	burst   float64 // bucket capacity
}

type bucket struct {
	tokens float64
	last   time.Time
}

// newRateLimiter returns a limiter for perMin requests/key/minute, or nil (off)
// when perMin <= 0. Burst is ~10s of headroom so normal admin polling never trips.
func newRateLimiter(perMin int) *rateLimiter {
	if perMin <= 0 {
		return nil
	}
	burst := float64(perMin) / 6.0
	if burst < 20 {
		burst = 20
	}
	return &rateLimiter{
		buckets: make(map[string]*bucket),
		rate:    float64(perMin) / 60.0,
		burst:   burst,
	}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}
