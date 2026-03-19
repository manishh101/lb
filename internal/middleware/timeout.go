package middleware

import (
	"context"
	"net/http"
	"time"

	"intelligent-lb/internal/priority"
)

// NewPriorityTimeout creates a middleware that sets per-request timeouts
// based on the request's priority classification.
//
// Priority levels:
//   - HIGH: short timeout (default 5s) for critical, fast-path requests
//   - MEDIUM: medium timeout (default 10s)
//   - LOW: long timeout (default 20s) for background/bulk operations
//
// This ensures high-priority requests fail fast while low-priority requests
// get more time. Timeout values are configurable per middleware instance.
func NewPriorityTimeout(highSec, mediumSec, lowSec int) Middleware {
	if highSec <= 0 {
		highSec = 5
	}
	if mediumSec <= 0 {
		mediumSec = 10
	}
	if lowSec <= 0 {
		lowSec = 20
	}

	highTimeout := time.Duration(highSec) * time.Second
	mediumTimeout := time.Duration(mediumSec) * time.Second
	lowTimeout := time.Duration(lowSec) * time.Second

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Classify the priority using the same classifier as the proxy
			pri := priority.Classify(r.URL.Path, r.Header.Get("X-Priority"))

			var timeout time.Duration
			switch pri {
			case "HIGH":
				timeout = highTimeout
			case "MEDIUM":
				timeout = mediumTimeout
			default: // LOW and anything else
				timeout = lowTimeout
			}

			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			// Store the priority in context for downstream access
			ctx = context.WithValue(ctx, priorityCtxKey{}, pri)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// priorityCtxKey is the context key for the request priority.
type priorityCtxKey struct{}

// PriorityFromContext extracts the request priority from context.
func PriorityFromContext(ctx context.Context) string {
	if pri, ok := ctx.Value(priorityCtxKey{}).(string); ok {
		return pri
	}
	return ""
}
