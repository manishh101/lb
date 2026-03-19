package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"intelligent-lb/internal/balancer"
	"intelligent-lb/internal/health"
	"intelligent-lb/internal/logging"
	"intelligent-lb/internal/metrics"
	"intelligent-lb/internal/priority"
)


// Handler is the HTTP reverse proxy that routes requests to backend servers.
// It uses an optimized transport for high-throughput connection pooling
// and implements transparent retry logic for failed backend requests.
type Handler struct {
	router     *balancer.Router
	metrics    *metrics.Collector
	breakers   map[string]*health.Breaker
	client           *http.Client
	maxRetries       int
	perAttemptTimeout time.Duration
}

// New creates a new proxy Handler with a production-grade HTTP transport.
// The transport is tuned for high concurrency with aggressive connection
// pooling, matching patterns used in Envoy and NGINX proxy backends.
func New(r *balancer.Router, m *metrics.Collector, b map[string]*health.Breaker, maxRetries int, perAttemptTimeoutSec int) *Handler {
	transport := &http.Transport{
		MaxIdleConns:        200,              // Total idle connections across all hosts
		MaxIdleConnsPerHost: 50,               // Per-host idle connection pool size
		MaxConnsPerHost:     100,              // Max total connections per host
		IdleConnTimeout:     90 * time.Second, // How long idle connections stay in pool
		DisableCompression:  true,             // Backend responses are typically internal; skip decompression overhead
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second, // TCP connection timeout
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second, // Max wait for response headers
	}

	return &Handler{
		router:            r,
		metrics:           m,
		breakers:          b,
		maxRetries:        maxRetries,
		perAttemptTimeout: time.Duration(perAttemptTimeoutSec) * time.Second,
		client: &http.Client{
			Transport: transport,
			// Overall request timeout is removed; timeout is strictly handled per-attempt via context
		},
	}
}

// ServeHTTP handles each incoming request by classifying its priority,
// selecting a backend with retry logic, proxying the request, and recording metrics.
//
// Retry behavior (production pattern inspired by Envoy/HAProxy):
//   - On backend connection error or 5xx response, the LB transparently retries
//     on the next available healthy server (up to maxRetries attempts).
//   - Only the final attempt's result is surfaced to the client.
//   - The client's context is propagated so cancelled requests abort immediately.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Step 1: Classify request priority (URL-based + header-based)
	pri := priority.Classify(req.URL.Path, req.Header.Get("X-Priority"))

	// Step 2: Retry loop — try up to maxRetries different backends
	var lastErr error
	tried := make(map[string]bool) // track already-tried servers to avoid repeats

	for attempt := 0; attempt < h.maxRetries; attempt++ {
		target, err := h.router.Select(pri)
		if err != nil {
			lastErr = err
			break // no healthy servers at all
		}

		// Skip servers we already tried this request cycle
		if tried[target] {
			continue
		}
		tried[target] = true

		// Attempt the proxy request
		success, done := h.proxyToBackend(w, req, target, pri, attempt)
		if done {
			return // response written to client successfully (or 5xx from backend on last retry)
		}
		if !success {
			lastErr = fmt.Errorf("backend %s failed", target)
			logging.Error(logging.AccessLog{
				Message: "Backend failed, trying next server",
				Method:  req.Method,
				Path:    req.URL.Path,
				Target:  target,
				Attempt: attempt + 1,
			})
			continue
		}
		return
	}

	// All retries exhausted
	http.Error(w, "All backend servers unavailable: "+lastErr.Error(), http.StatusBadGateway)
	logging.Error(logging.AccessLog{
		Message:  "ALL RETRIES EXHAUSTED - 502 returned to client",
		Method:   req.Method,
		Path:     req.URL.Path,
		Priority: pri,
		Error:    lastErr.Error(),
	})
}

// proxyToBackend forwards a single request to the given target server.
// Returns (success, responseSent):
//   - success=true:  backend responded with < 500 status
//   - success=false: backend unreachable or returned 5xx (retryable)
//   - responseSent=true: a response has already been written to the client (do not retry)
func (h *Handler) proxyToBackend(
	w http.ResponseWriter,
	req *http.Request,
	target, pri string,
	attempt int,
) (success bool, responseSent bool) {
	h.metrics.RecordStart(target)
	start := time.Now()

	// Build proxy request using the client's context for cancellation propagation
	// and a per-attempt timeout (Gap 7 fix, inspired by Caddy retry isolation)
	ctx, cancel := context.WithTimeout(req.Context(), h.perAttemptTimeout)
	defer cancel()

	url := fmt.Sprintf("%s%s", target, req.RequestURI)
	proxyReq, err := http.NewRequestWithContext(ctx, req.Method, url, req.Body)
	if err != nil {
		h.metrics.RecordEnd(target, 0, false)
		// This is a programming error, not a backend failure — don't retry
		http.Error(w, "Failed to build proxy request", http.StatusInternalServerError)
		return false, true
	}

	// Copy all original headers
	for k, vals := range req.Header {
		proxyReq.Header[k] = vals
	}
	proxyReq.Header.Set("X-Forwarded-Host", req.Host)
	proxyReq.Header.Set("X-Forwarded-For", req.RemoteAddr)

	// Execute the request to the backend
	resp, err := h.client.Do(proxyReq)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		h.metrics.RecordEnd(target, latencyMs, false)
		h.breakers[target].RecordFailure()
		h.metrics.SetCircuitState(target, h.breakers[target].State())
		
		logging.Error(logging.AccessLog{
			Message:   "Proxy request failed",
			Method:    req.Method,
			Path:      req.URL.Path,
			Priority:  pri,
			Target:    target,
			LatencyMs: latencyMs,
			Attempt:   attempt + 1,
			Error:     err.Error(),
		})
		
		return false, false // retryable failure
	}
	defer resp.Body.Close()

	// Determine success: anything < 500 is a success
	isSuccess := resp.StatusCode < 500
	h.metrics.RecordEnd(target, latencyMs, isSuccess)

	if isSuccess {
		h.breakers[target].RecordSuccess()
	} else {
		h.breakers[target].RecordFailure()
	}
	h.metrics.SetCircuitState(target, h.breakers[target].State())
	h.metrics.RecordPriority(target, pri)

	// If backend returned 5xx and we haven't exhausted retries, signal for retry
	if !isSuccess && attempt < h.maxRetries-1 {
		// Drain the body so the connection can be reused
		io.Copy(io.Discard, resp.Body)
		
		logging.Error(logging.AccessLog{
			Message:    "Proxy returned 5xx, will retry",
			Method:     req.Method,
			Path:       req.URL.Path,
			Priority:   pri,
			Target:     target,
			StatusCode: resp.StatusCode,
			LatencyMs:  latencyMs,
			Attempt:    attempt + 1,
		})
		
		return false, false
	}

	// Write the response to the client
	serverName := h.metrics.GetName(target)
	for k, vals := range resp.Header {
		w.Header()[k] = vals
	}
	w.Header().Set("X-Handled-By", serverName)
	w.Header().Set("X-Retry-Count", fmt.Sprintf("%d", attempt))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	logging.Info(logging.AccessLog{
		Message:    "Request proxied successfully",
		Method:     req.Method,
		Path:       req.URL.Path,
		Priority:   pri,
		ServerName: serverName,
		Target:     target,
		StatusCode: resp.StatusCode,
		LatencyMs:  latencyMs,
		Attempt:    attempt + 1,
	})
	
	return isSuccess, true
}
