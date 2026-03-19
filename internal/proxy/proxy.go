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
	"intelligent-lb/internal/middleware"
	"intelligent-lb/internal/priority"
)

// Handler is the HTTP reverse proxy that routes requests to backend servers.
// It uses an optimized transport for high-throughput connection pooling.
// Retry logic and exponential backoff are handled by the retry middleware
// upstream in the pipeline — this handler focuses on single-attempt proxying.
type Handler struct {
	router            *balancer.Router
	metrics           *metrics.Collector
	breakers          map[string]*health.Breaker
	client            *http.Client
	maxRetries        int
	perAttemptTimeout time.Duration
}

// New creates a new proxy Handler with a production-grade HTTP transport.
// The transport is tuned for high concurrency with aggressive connection
// pooling, matching patterns used in Envoy and NGINX proxy backends.
func New(r *balancer.Router, m *metrics.Collector, b map[string]*health.Breaker,
	maxRetries int, perAttemptTimeoutSec int,
) *Handler {
	transport := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 50,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second,
	}

	return &Handler{
		router:            r,
		metrics:           m,
		breakers:          b,
		maxRetries:        maxRetries,
		perAttemptTimeout: time.Duration(perAttemptTimeoutSec) * time.Second,
		client: &http.Client{
			Transport: transport,
		},
	}
}

// ServeHTTP handles each incoming request by classifying its priority,
// selecting a backend, proxying the request, and recording metrics.
//
// This is the final handler in the middleware chain. The retry middleware
// upstream handles retry logic and exponential backoff. The timeout
// middleware sets context deadlines based on priority.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Extract request ID and client IP from middleware-enriched headers
	requestID := middleware.RequestIDFromContext(req.Context())
	clientIP := req.Header.Get("X-Real-IP")
	if clientIP == "" {
		clientIP = req.RemoteAddr
	}

	// Classify request priority (also done by timeout middleware, but needed for routing)
	pri := priority.Classify(req.URL.Path, req.Header.Get("X-Priority"))

	// Get the current retry attempt from context (set by retry middleware)
	attempt := middleware.AttemptFromContext(req.Context())

	// Get excluded backend URLs (set by retry middleware)
	excluded := middleware.ExcludedFromContext(req.Context())

	// Select a backend server
	target, err := h.router.Select(pri, excluded)
	if err != nil {
		http.Error(w, "All backend servers unavailable: "+err.Error(), http.StatusBadGateway)
		logging.Error(logging.AccessLog{
			Message:   "No healthy servers available",
			RequestID: requestID,
			ClientIP:  clientIP,
			Method:    req.Method,
			Path:      req.URL.Path,
			Priority:  pri,
			Error:     err.Error(),
		})
		return
	}

	// Proxy to the selected backend
	h.proxyToBackend(w, req, target, pri, attempt, requestID, clientIP)
}

// proxyToBackend forwards a single request to the given target server.
func (h *Handler) proxyToBackend(
	w http.ResponseWriter,
	req *http.Request,
	target, pri string,
	attempt int,
	requestID, clientIP string,
) {
	h.metrics.RecordStart(target)
	start := time.Now()

	// Use per-attempt timeout from context if set by timeout middleware,
	// otherwise fall back to configured per-attempt timeout
	timeout := h.perAttemptTimeout
	if deadline, ok := req.Context().Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < timeout {
			timeout = remaining
		}
	}

	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()

	url := fmt.Sprintf("%s%s", target, req.RequestURI)
	proxyReq, err := http.NewRequestWithContext(ctx, req.Method, url, req.Body)
	if err != nil {
		h.metrics.RecordEnd(target, 0, false)
		http.Error(w, "Failed to build proxy request", http.StatusInternalServerError)
		return
	}

	// Copy all original headers (including enriched headers from middleware)
	for k, vals := range req.Header {
		proxyReq.Header[k] = vals
	}

	// Execute the request to the backend
	resp, err := h.client.Do(proxyReq)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		h.metrics.RecordEnd(target, latencyMs, false)
		// Circuit breaker failure recording is now handled by the circuitbreaker middleware.

		logging.Error(logging.AccessLog{
			Message:   "Proxy request failed",
			RequestID: requestID,
			ClientIP:  clientIP,
			Method:    req.Method,
			Path:      req.URL.Path,
			Priority:  pri,
			Target:    target,
			LatencyMs: latencyMs,
			Attempt:   attempt,
			Error:     err.Error(),
		})

		http.Error(w, "Backend server error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	isSuccess := resp.StatusCode < 500
	h.metrics.RecordEnd(target, latencyMs, isSuccess)
	h.metrics.RecordPriority(target, pri)

	// Write the response to the client
	serverName := h.metrics.GetName(target)
	for k, vals := range resp.Header {
		w.Header()[k] = vals
	}
	w.Header().Set("X-Handled-By", serverName)
	w.Header().Set("X-Backend-URL", target)
	w.Header().Set("X-Retry-Count", fmt.Sprintf("%d", attempt-1))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	logging.Info(logging.AccessLog{
		Message:    "Request proxied successfully",
		RequestID:  requestID,
		ClientIP:   clientIP,
		Method:     req.Method,
		Path:       req.URL.Path,
		Priority:   pri,
		ServerName: serverName,
		Target:     target,
		StatusCode: resp.StatusCode,
		LatencyMs:  latencyMs,
		Attempt:    attempt,
	})
}
