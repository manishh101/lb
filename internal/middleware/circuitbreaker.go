package middleware

import (
	"net/http"
)

// circuitBreakerMiddleware implements the circuit breaker pattern as a middleware.
// It acts as an interface to the service-level circuit breakers. It inspects the
// response to determine the backend used, and records success or failure directly
// on the backend's native breaker. This prevents state duplication and ensures
// the middleware correctly interfaces with the per-server breakers.
type circuitBreakerMiddleware struct {
	registry ServiceRegistry
}

// cbStatusRecorder wraps ResponseWriter to capture the status code.
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

// NewCircuitBreaker creates a circuit breaker middleware that delegates to
// the service-level per-backend health breakers.
func NewCircuitBreaker(registry ServiceRegistry) Middleware {
	cb := &circuitBreakerMiddleware{
		registry: registry,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder := &cbStatusRecorder{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			next.ServeHTTP(recorder, r)

			// Get the backend URL that handled this request
			backendURL := recorder.Header().Get("X-Backend-URL")
			if backendURL == "" {
				return // Request didn't reach a backend
			}

			// Record success/failure directly on the backend's breaker
			if cb.registry != nil {
				cb.registry.RecordCircuitBreakerResult(backendURL, recorder.statusCode < 500)
			}
		})
	}
}
