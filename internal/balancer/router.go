package balancer

import (
	"errors"

	"intelligent-lb/internal/health"
	"intelligent-lb/internal/metrics"
)

// Router selects the best backend server for each incoming request.
// It filters candidates using IsOpen() (pure read, no side effects) and
// only calls CanSend() (which may transition OPEN→HALF_OPEN) on the
// single chosen server. (FIX B5)
type Router struct {
	servers  []string
	metrics  *metrics.Collector
	breakers map[string]*health.Breaker
	algo     Algorithm
}

// NewRouter creates a Router with the given servers, metrics, breakers, and algorithm.
func NewRouter(servers []string, m *metrics.Collector, b map[string]*health.Breaker, algo Algorithm) *Router {
	return &Router{
		servers:  servers,
		metrics:  m,
		breakers: b,
		algo:     algo,
	}
}

// Select picks the best healthy server for the given priority level,
// excluding any URLs passed in the excluded slice.
func (r *Router) Select(priority string, excluded []string) (string, error) {
	stats := r.metrics.Snapshot()

	excludedMap := make(map[string]bool)
	for _, e := range excluded {
		excludedMap[e] = true
	}

	// FIX B5: Use IsOpen() (pure check, no side-effect) for candidate filtering
	var candidates []string
	for _, url := range r.servers {
		if excludedMap[url] {
			continue
		}
		s, ok := stats[url]
		if !ok {
			continue
		}
		// Include server if healthy AND circuit is not OPEN
		if s.IsHealthy && !r.breakers[url].IsOpen() {
			candidates = append(candidates, url)
		}
	}

	if len(candidates) == 0 {
		return "", errors.New("no healthy servers available")
	}

	chosen := r.algo.Select(candidates, stats, priority)
	if chosen == "" {
		return "", errors.New("routing algorithm returned no server")
	}

	// FIX B5: Only call CanSend() (which may trigger OPEN→HALF_OPEN) on chosen server
	if !r.breakers[chosen].CanSend() {
		return "", errors.New("chosen server circuit is open")
	}
	return chosen, nil
}
