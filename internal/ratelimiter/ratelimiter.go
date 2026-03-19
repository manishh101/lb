package ratelimiter

import (
	"net/http"

	"golang.org/x/time/rate"
)

// RateLimiter uses a Token Bucket to shape/limit incoming traffic.
type RateLimiter struct {
	limiter *rate.Limiter
}

// New creates a new global RateLimiter.
// rps is the allowed Requests Per Second.
// burst is the maximum allowed burst size.
func New(rps float64, burst int) *RateLimiter {
	limiter := rate.NewLimiter(rate.Limit(rps), burst)
	return &RateLimiter{
		limiter: limiter,
	}
}

// Middleware wraps an http.Handler and enforcing standard HTTP 429 semantics
// with a Retry-After header when the limit is exceeded.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow() consumes 1 token. Returns false if no tokens are available.
		if !rl.limiter.Allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Allow allows manual token consumption, useful for unit tests or manual integration.
func (rl *RateLimiter) Allow() bool {
	return rl.limiter.Allow()
}

// SetLimit updates the rate limit dynamically.
func (rl *RateLimiter) SetLimit(rps float64, burst int) {
	rl.limiter.SetLimit(rate.Limit(rps))
	rl.limiter.SetBurst(burst)
}
