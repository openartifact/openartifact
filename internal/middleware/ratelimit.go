// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// tokenBucket implements a simple token bucket for rate limiting.
type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
	limit      int
	window     time.Duration
}

// RateLimitMiddleware limits requests per IP using a token bucket algorithm.
// limit: max requests per window
// window: time window duration
// trusted: which reverse proxies' X-Forwarded-For/X-Real-IP are honored
// when resolving the client IP — see ClientIP.
func RateLimitMiddleware(limit int, window time.Duration, trusted *TrustedProxies) func(http.Handler) http.Handler {
	buckets := make(map[string]*tokenBucket)

	var mu sync.Mutex

	// Cleanup goroutine to remove stale buckets
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic in goroutine: %v", r)
			}
		}()
		ticker := time.NewTicker(window * 2)
		defer ticker.Stop()

		for range ticker.C {
			mu.Lock()

			now := time.Now()
			for ip, bucket := range buckets {
				if now.Sub(bucket.lastRefill) > window*2 {
					delete(buckets, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := LimiterKey(ClientIP(r, trusted))

			mu.Lock()

			bucket, exists := buckets[clientIP]
			if !exists {
				bucket = &tokenBucket{
					tokens:     float64(limit),
					lastRefill: time.Now(),
					limit:      limit,
					window:     window,
				}
				buckets[clientIP] = bucket
			}

			// Refill tokens based on elapsed time
			now := time.Now()
			elapsed := now.Sub(bucket.lastRefill)
			tokensToAdd := float64(limit) * (elapsed.Seconds() / window.Seconds())
			bucket.tokens = minFloat64(float64(limit), bucket.tokens+tokensToAdd)
			bucket.lastRefill = now

			// Check if we have tokens
			allowed := bucket.tokens >= 1.0
			if allowed {
				bucket.tokens -= 1.0
			}
			mu.Unlock()

			if !allowed {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// minFloat64 returns the minimum of two floats.
func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}

	return b
}
