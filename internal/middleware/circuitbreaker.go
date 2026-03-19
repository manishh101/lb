package middleware

import (
	"log"
	"net/http"
	"sync"
	"time"
)

// circuitBreakerMiddleware implements the circuit breaker pattern as a middleware.
// It tracks request success/failure and opens the circuit when the failure
// threshold is reached, rejecting requests with 503 until the recovery timeout.
//
// State machine:
//   - CLOSED: normal operation, requests flow through
//   - OPEN: circuit tripped, requests immediately rejected with 503
//   - HALF_OPEN: recovery probe, one request allowed through to test
//
// Inspired by Traefik's circuitbreaker middleware and the existing health.Breaker.
type circuitBreakerMiddleware struct {
	mu              sync.Mutex
	state           cbState
	failureCount    int
	threshold       int
	recoveryTimeout time.Duration
	lastFailureTime time.Time
}

type cbState int

const (
	cbClosed   cbState = iota // Normal
	cbOpen                    // Tripped
	cbHalfOpen                // Recovery probe
)

// cbStatusRecorder wraps ResponseWriter to capture the status code for
// circuit breaker failure/success tracking.
type cbStatusRecorder struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (sr *cbStatusRecorder) WriteHeader(code int) {
	if !sr.written {
		sr.statusCode = code
		sr.written = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *cbStatusRecorder) Write(b []byte) (int, error) {
	if !sr.written {
		sr.statusCode = http.StatusOK
		sr.written = true
	}
	return sr.ResponseWriter.Write(b)
}

// NewCircuitBreaker creates a circuit breaker middleware with the given
// failure threshold and recovery timeout.
//
// Parameters:
//   - threshold: number of consecutive failures before opening the circuit
//   - recoveryTimeoutSec: seconds to wait before allowing a probe request
func NewCircuitBreaker(threshold, recoveryTimeoutSec int) Middleware {
	if threshold <= 0 {
		threshold = 3
	}
	if recoveryTimeoutSec <= 0 {
		recoveryTimeoutSec = 15
	}

	cb := &circuitBreakerMiddleware{
		state:           cbClosed,
		threshold:       threshold,
		recoveryTimeout: time.Duration(recoveryTimeoutSec) * time.Second,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cb.canSend() {
				log.Printf("[CIRCUIT-BREAKER] Request rejected: circuit OPEN for %s %s", r.Method, r.URL.Path)
				w.Header().Set("Retry-After", "5")
				http.Error(w, "Service Unavailable (circuit breaker open)", http.StatusServiceUnavailable)
				return
			}

			recorder := &cbStatusRecorder{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			next.ServeHTTP(recorder, r)

			// Track success/failure
			if recorder.statusCode >= 500 {
				cb.recordFailure()
			} else {
				cb.recordSuccess()
			}
		})
	}
}

// canSend checks if a request should be allowed through.
// May transition OPEN → HALF_OPEN when recovery timeout has elapsed.
func (cb *circuitBreakerMiddleware) canSend() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case cbClosed:
		return true
	case cbOpen:
		if time.Since(cb.lastFailureTime) > cb.recoveryTimeout {
			cb.state = cbHalfOpen
			log.Printf("[CIRCUIT-BREAKER] Transitioning to HALF_OPEN (recovery probe)")
			return true
		}
		return false
	case cbHalfOpen:
		return true
	}
	return false
}

// recordSuccess resets the circuit breaker to CLOSED state.
func (cb *circuitBreakerMiddleware) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == cbHalfOpen {
		log.Printf("[CIRCUIT-BREAKER] Probe succeeded, transitioning to CLOSED")
	}
	cb.failureCount = 0
	cb.state = cbClosed
}

// recordFailure increments the failure counter and trips the circuit
// if the threshold is reached or if in HALF_OPEN state.
func (cb *circuitBreakerMiddleware) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()

	if cb.failureCount >= cb.threshold || cb.state == cbHalfOpen {
		if cb.state != cbOpen {
			log.Printf("[CIRCUIT-BREAKER] Circuit OPENED after %d failures", cb.failureCount)
		}
		cb.state = cbOpen
	}
}
