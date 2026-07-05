package api

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// tokenBucket is a simple thread-safe token-bucket rate limiter.
type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	max        float64
	refillRate float64 // tokens per second
	last       time.Time
}

func newTokenBucket(ratePerSec float64, burst int) *tokenBucket {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &tokenBucket{
		tokens:     float64(burst),
		max:        float64(burst),
		refillRate: ratePerSec,
		last:       time.Now(),
	}
}

// allow reports whether one token is available, consuming it if so.
func (b *tokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.max {
		b.tokens = b.max
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// keyedLimiter holds one token bucket per key (e.g. client IP) with lazy
// eviction of idle buckets so memory does not grow unbounded.
type keyedLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*bucketEntry
	ratePerSec float64
	burst      int
	lastSweep  time.Time
}

type bucketEntry struct {
	bucket *tokenBucket
	seen   time.Time
}

func newKeyedLimiter(ratePerSec float64, burst int) *keyedLimiter {
	return &keyedLimiter{
		buckets:    map[string]*bucketEntry{},
		ratePerSec: ratePerSec,
		burst:      burst,
		lastSweep:  time.Now(),
	}
}

func (k *keyedLimiter) allow(key string, now time.Time) bool {
	k.mu.Lock()
	e, ok := k.buckets[key]
	if !ok {
		e = &bucketEntry{bucket: newTokenBucket(k.ratePerSec, k.burst)}
		k.buckets[key] = e
	}
	e.seen = now
	if now.Sub(k.lastSweep) > 5*time.Minute {
		for kk, ee := range k.buckets {
			if now.Sub(ee.seen) > 10*time.Minute {
				delete(k.buckets, kk)
			}
		}
		k.lastSweep = now
	}
	k.mu.Unlock()
	return e.bucket.allow(now)
}

// perIPLimit rejects requests from a client IP that exceeds the limiter's rate,
// bounding brute-force of API keys / admin token and general abuse.
func perIPLimit(k *keyedLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !k.allow(clientIP(r), time.Now()) {
			tooManyRequests(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// globalLimit rejects requests once the shared bucket is empty (used to cap the
// overall ingest rate for the whole process).
func globalLimit(b *tokenBucket, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !b.allow(time.Now()) {
			tooManyRequests(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(1))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
}

// clientIP extracts the client's IP from RemoteAddr. When AlertLoop runs behind
// a reverse proxy, the proxy is expected to enforce its own limits; app-level
// per-IP limiting is a defense-in-depth for direct exposure.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
