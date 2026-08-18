package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// RateLimiter is a fixed-window counter keyed by an arbitrary string
// (IP, username, pending-2FA token). Good enough for brute-force protection
// on auth endpoints; not meant as a general traffic shaper.
type RateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*bucket
}

type bucket struct {
	count int
	reset time.Time
}

func NewRateLimiter(max int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{max: max, window: window, hits: map[string]*bucket{}}
	go func() {
		for range time.Tick(5 * time.Minute) {
			rl.mu.Lock()
			now := time.Now()
			for k, b := range rl.hits {
				if now.After(b.reset) {
					delete(rl.hits, k)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

// maxTrackedKeys bounds the number of distinct keys held at once. Keys can
// derive from untrusted input, so without a cap an attacker could grow the
// map without bound by varying the key on every request.
const maxTrackedKeys = 10_000

// Allow records a hit for key and reports whether it is within the limit.
func (rl *RateLimiter) Allow(key string) bool {
	// Never retain attacker-sized keys: hash anything beyond a sane length.
	if len(key) > 128 {
		sum := sha256.Sum256([]byte(key))
		key = hex.EncodeToString(sum[:8])
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if _, exists := rl.hits[key]; !exists && len(rl.hits) >= maxTrackedKeys {
		// Table is saturated. Drop expired entries first; if that frees
		// nothing, fail closed so a key-flood cannot grow the map.
		for k, b := range rl.hits {
			if now.After(b.reset) {
				delete(rl.hits, k)
			}
		}
		if len(rl.hits) >= maxTrackedKeys {
			return false
		}
	}
	b, ok := rl.hits[key]
	if !ok || now.After(b.reset) {
		rl.hits[key] = &bucket{count: 1, reset: now.Add(rl.window)}
		return true
	}
	b.count++
	return b.count <= rl.max
}

// Reset clears the counter for key (e.g. after successful login).
func (rl *RateLimiter) Reset(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.hits, key)
}
