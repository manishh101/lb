package middleware

import (
	"context"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"intelligent-lb/internal/logging"
)

// retryMiddleware retries failed requests with exponential backoff.
// Only retries on connection errors and 5xx responses; never on 4xx.
// Inspired by Traefik's retry middleware (pkg/middlewares/retry/retry.go).
type retryMiddleware struct {
	maxAttempts       int
	initialIntervalMs int
}

// retryResponseWriter captures the response to detect 5xx errors before
// committing the response to the client. It buffers headers and status code.
type retryResponseWriter struct {
	http.ResponseWriter
	statusCode int
	body       []byte
	headers    http.Header
	committed  bool
}

func newRetryResponseWriter(w http.ResponseWriter) *retryResponseWriter {
	return &retryResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		headers:        make(http.Header),
	}
}

func (rw *retryResponseWriter) Header() http.Header {
	return rw.headers
}

func (rw *retryResponseWriter) WriteHeader(code int) {
	if !rw.committed {
		rw.statusCode = code
	}
}

func (rw *retryResponseWriter) Write(b []byte) (int, error) {
	if !rw.committed {
		rw.body = append(rw.body, b...)
		return len(b), nil
	}
	return rw.ResponseWriter.Write(b)
}

// flush writes the buffered response to the real ResponseWriter.
func (rw *retryResponseWriter) flush() {
	if rw.committed {
		return
	}
	rw.committed = true

	// Copy buffered headers to the real writer
	for k, vals := range rw.headers {
		for _, v := range vals {
			rw.ResponseWriter.Header().Add(k, v)
		}
	}
	rw.ResponseWriter.WriteHeader(rw.statusCode)
	if len(rw.body) > 0 {
		rw.ResponseWriter.Write(rw.body)
	}
}

// isRetryable returns true if the status code indicates a retryable server error.
func isRetryable(statusCode int) bool {
	return statusCode >= 500
}

// NewRetry creates a retry middleware with exponential backoff.
// Parameters:
//   - maxAttempts: maximum number of total attempts (including the first)
//   - initialIntervalMs: initial backoff interval in milliseconds (doubles each retry)
func NewRetry(maxAttempts int, initialIntervalMs int) Middleware {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if initialIntervalMs <= 0 {
		initialIntervalMs = 100
	}

	rm := &retryMiddleware{
		maxAttempts:       maxAttempts,
		initialIntervalMs: initialIntervalMs,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := RequestIDFromContext(r.Context())

			var lastWriter *retryResponseWriter

			for attempt := 1; attempt <= rm.maxAttempts; attempt++ {
				// Exponential backoff before retry (skip on first attempt)
				if attempt > 1 {
					backoff := rm.calculateBackoff(attempt - 1)

					logging.Info(logging.AccessLog{
						Message:   "Retry middleware: backoff before retry",
						RequestID: requestID,
						Method:    r.Method,
						Path:      r.URL.Path,
						Attempt:   attempt,
						BackoffMs: backoff.Milliseconds(),
					})

					select {
					case <-time.After(backoff):
						// Backoff complete
					case <-r.Context().Done():
						http.Error(w, "Client disconnected during retry backoff", http.StatusGatewayTimeout)
						return
					}
				}

				// Create a buffering response writer to capture the response
				lastWriter = newRetryResponseWriter(w)

				// Clone the request context with attempt info
				ctx := context.WithValue(r.Context(), retryAttemptKey{}, attempt)
				attemptReq := r.WithContext(ctx)

				next.ServeHTTP(lastWriter, attemptReq)

				// Check if we should retry
				if !isRetryable(lastWriter.statusCode) || attempt == rm.maxAttempts {
					// Success or client error or last attempt — commit response
					break
				}

				// Log the retry
				logging.Info(logging.AccessLog{
					Message:    "Retry middleware: got retryable response, retrying",
					RequestID:  requestID,
					Method:     r.Method,
					Path:       r.URL.Path,
					StatusCode: lastWriter.statusCode,
					Attempt:    attempt,
				})

				// Reset the buffered writer for next attempt
				lastWriter = nil
			}

			// Commit the final response
			if lastWriter != nil {
				// Add X-Attempts header
				lastWriter.headers.Set("X-Attempts", strconv.Itoa(getCurrentAttempt(r.Context())))
				lastWriter.flush()
			}
		})
	}
}

// calculateBackoff computes the exponential backoff duration.
// Formula: initialInterval * 2^(attempt-1) + jitter (0-25%).
func (rm *retryMiddleware) calculateBackoff(attempt int) time.Duration {
	base := float64(rm.initialIntervalMs) * math.Pow(2, float64(attempt-1))
	jitter := rand.Float64() * 0.25 * base
	return time.Duration(base+jitter) * time.Millisecond
}

// retryAttemptKey is the context key for the current retry attempt number.
type retryAttemptKey struct{}

// getCurrentAttempt returns the current retry attempt from context.
func getCurrentAttempt(ctx context.Context) int {
	if attempt, ok := ctx.Value(retryAttemptKey{}).(int); ok {
		return attempt
	}
	return 1
}

// AttemptFromContext returns the current retry attempt number from the request context.
func AttemptFromContext(ctx context.Context) int {
	return getCurrentAttempt(ctx)
}
