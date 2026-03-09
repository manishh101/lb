package proxy

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"intelligent-lb/internal/balancer"
	"intelligent-lb/internal/health"
	"intelligent-lb/internal/metrics"
	"intelligent-lb/internal/priority"
)

// Handler is the HTTP reverse proxy that routes requests to backend servers.
type Handler struct {
	router   *balancer.Router
	metrics  *metrics.Collector
	breakers map[string]*health.Breaker
	client   *http.Client
}

// New creates a new proxy Handler.
func New(r *balancer.Router, m *metrics.Collector, b map[string]*health.Breaker) *Handler {
	return &Handler{
		router:   r,
		metrics:  m,
		breakers: b,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// ServeHTTP handles each incoming request by classifying its priority,
// selecting a backend, proxying the request, and recording metrics.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// FIX B6: Use priority.Classify() for URL-based + header-based classification
	pri := priority.Classify(req.URL.Path, req.Header.Get("X-Priority"))

	target, err := h.router.Select(pri)
	if err != nil {
		http.Error(w, "No servers available: "+err.Error(), http.StatusBadGateway)
		log.Printf("[PROXY] No healthy servers: %v", err)
		return
	}

	h.metrics.RecordStart(target)
	start := time.Now()

	// FIX B1: Check NewRequest error explicitly — never discard with _
	url := fmt.Sprintf("%s%s", target, req.RequestURI)
	proxyReq, err := http.NewRequest(req.Method, url, req.Body)
	if err != nil {
		h.metrics.RecordEnd(target, 0, false)
		http.Error(w, "Failed to build proxy request", http.StatusInternalServerError)
		return
	}
	for k, vals := range req.Header {
		proxyReq.Header[k] = vals
	}
	proxyReq.Header.Set("X-Forwarded-Host", req.Host)

	resp, err := h.client.Do(proxyReq)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		h.metrics.RecordEnd(target, latencyMs, false)
		h.breakers[target].RecordFailure()
		h.metrics.SetCircuitState(target, h.breakers[target].State())
		http.Error(w, "Backend error", http.StatusBadGateway)
		log.Printf("[PROXY] %-8s %-30s FAIL  %.1fms", pri, target, latencyMs)
		return
	}
	defer resp.Body.Close()

	h.metrics.RecordEnd(target, latencyMs, true)
	h.breakers[target].RecordSuccess()
	h.metrics.SetCircuitState(target, h.breakers[target].State())
	h.metrics.RecordPriority(target, pri)

	// FIX B2: Set X-Handled-By so client can track server distribution
	serverName := h.metrics.GetName(target)
	w.Header().Set("X-Handled-By", serverName)
	for k, vals := range resp.Header {
		w.Header()[k] = vals
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	log.Printf("[PROXY] %-4s %-8s %-25s %3d  %.1fms",
		pri, serverName, target, resp.StatusCode, latencyMs)
}
