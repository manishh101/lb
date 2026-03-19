package metrics

import (
	"sort"
	"sync"
	"time"
)

// CircuitEvent records a circuit breaker state transition.
type CircuitEvent struct {
	ServerName string `json:"server_name"`
	ServerURL  string `json:"server_url"`
	OldState   string `json:"old_state"`
	NewState   string `json:"new_state"`
	Timestamp  string `json:"timestamp"`
}

// ServerStats holds all metrics for a single backend server.
type ServerStats struct {
	Name              string  `json:"name"`
	URL               string  `json:"url"`
	Weight            int     `json:"weight"`
	IsHealthy         bool    `json:"is_healthy"`
	AvgLatencyMs      float64 `json:"avg_latency_ms"`
	P95LatencyMs      float64 `json:"p95_latency_ms"`
	ActiveConnections int64   `json:"active_connections"`
	TotalRequests     int64   `json:"total_requests"`
	SuccessCount      int64   `json:"success_count"`
	FailureCount      int64   `json:"failure_count"`
	RetryCount        int64   `json:"retry_count"`
	CircuitState      string  `json:"circuit_state"`
	HighPriorityCount int64   `json:"high_priority_count"`
	LowPriorityCount  int64   `json:"low_priority_count"`
	LastChecked       string  `json:"last_checked"`
	recentLatencies   []float64
}

// DashboardSnapshot is the enriched snapshot sent to the dashboard.
// It wraps per-server stats with global computed metrics.
type DashboardSnapshot struct {
	Servers       map[string]ServerStats `json:"servers"`
	GlobalRPS     float64                `json:"global_rps"`
	TotalRequests int64                  `json:"total_requests"`
	SuccessRate   float64                `json:"success_rate"`
	HealthyCount  int                    `json:"healthy_count"`
	TotalCount    int                    `json:"total_count"`
	Algorithm     string                 `json:"algorithm"`
	CircuitEvents []CircuitEvent         `json:"circuit_events"`
}

// Collector is a thread-safe metrics store for all backend servers.
type Collector struct {
	mu      sync.RWMutex
	servers map[string]*ServerStats

	// RPS computation state
	lastSnapshotTime  time.Time
	lastTotalRequests int64

	// Circuit breaker event log (ring buffer, max 50)
	circuitEvents []CircuitEvent

	// Global algorithm name for dashboard display
	algorithm string
}

// New creates a Collector and initializes metrics for each server.
func New(servers []string, names []string, weights []int) *Collector {
	c := &Collector{
		servers:          make(map[string]*ServerStats),
		lastSnapshotTime: time.Now(),
	}
	for i, url := range servers {
		name := url
		weight := 1
		if i < len(names) {
			name = names[i]
		}
		if i < len(weights) && weights[i] > 0 {
			weight = weights[i]
		}
		c.servers[url] = &ServerStats{
			Name:         name,
			URL:          url,
			Weight:       weight,
			IsHealthy:    true,
			CircuitState: "CLOSED",
		}
	}
	return c
}

// SetAlgorithm stores the algorithm name for dashboard display.
func (c *Collector) SetAlgorithm(algo string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.algorithm = algo
}

// RecordStart increments the active connection count for a server.
func (c *Collector) RecordStart(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.servers[url]; ok {
		s.ActiveConnections++
	}
}

// RecordEnd decrements active connections, records latency, computes P95, and updates success/failure.
func (c *Collector) RecordEnd(url string, latencyMs float64, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.servers[url]
	if !ok {
		return
	}
	s.ActiveConnections--
	if s.ActiveConnections < 0 {
		s.ActiveConnections = 0
	}
	s.TotalRequests++

	// Rolling latency window (max 100 samples for P95 calculation)
	s.recentLatencies = append(s.recentLatencies, latencyMs)
	if len(s.recentLatencies) > 100 {
		s.recentLatencies = s.recentLatencies[1:]
	}

	// Compute average latency
	sum := 0.0
	for _, v := range s.recentLatencies {
		sum += v
	}
	s.AvgLatencyMs = sum / float64(len(s.recentLatencies))

	// Compute P95 latency: sort a copy, take element at index int(0.95 * len)
	s.P95LatencyMs = computeP95(s.recentLatencies)

	if success {
		s.SuccessCount++
	} else {
		s.FailureCount++
	}
}

// computeP95 calculates the 95th percentile from a slice of latency values.
func computeP95(latencies []float64) float64 {
	n := len(latencies)
	if n == 0 {
		return 0
	}
	// Make a copy to avoid mutating the original slice
	sorted := make([]float64, n)
	copy(sorted, latencies)
	sort.Float64s(sorted)

	// P95 index: int(0.95 * n) clamped to [0, n-1]
	idx := int(float64(n) * 0.95)
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

// RecordRetry increments the retry counter for a server.
// Called when a request is retried away from this server.
func (c *Collector) RecordRetry(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.servers[url]; ok {
		s.RetryCount++
	}
}

// SetHealth marks a server as healthy or unhealthy.
func (c *Collector) SetHealth(url string, healthy bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.servers[url]; ok {
		s.IsHealthy = healthy
		s.LastChecked = time.Now().Format("15:04:05")
	}
}

// SetCircuitState updates the circuit breaker state for a server
// and records a circuit event if the state changed.
func (c *Collector) SetCircuitState(url string, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.servers[url]
	if !ok {
		return
	}
	oldState := s.CircuitState
	s.CircuitState = state

	// Record event if state actually changed
	if oldState != state {
		event := CircuitEvent{
			ServerName: s.Name,
			ServerURL:  url,
			OldState:   oldState,
			NewState:   state,
			Timestamp:  time.Now().Format("15:04:05"),
		}
		c.circuitEvents = append(c.circuitEvents, event)
		// Keep last 50 events
		if len(c.circuitEvents) > 50 {
			c.circuitEvents = c.circuitEvents[len(c.circuitEvents)-50:]
		}
	}
}

// RecordPriority tracks HIGH vs LOW request counts per server.
func (c *Collector) RecordPriority(url, priority string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.servers[url]; ok {
		if priority == "HIGH" {
			s.HighPriorityCount++
		} else {
			s.LowPriorityCount++
		}
	}
}

// GetName returns the display name for a server URL.
func (c *Collector) GetName(url string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if s, ok := c.servers[url]; ok {
		return s.Name
	}
	return url
}

// Snapshot returns a copy of all server stats for thread-safe reading.
func (c *Collector) Snapshot() map[string]ServerStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap := make(map[string]ServerStats, len(c.servers))
	for url, s := range c.servers {
		snap[url] = *s
	}
	return snap
}

// DashboardSnap returns an enriched snapshot with global metrics for the dashboard.
func (c *Collector) DashboardSnap() DashboardSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	snap := make(map[string]ServerStats, len(c.servers))
	var totalReq, totalOK int64
	healthyCount := 0

	for url, s := range c.servers {
		snap[url] = *s
		totalReq += s.TotalRequests
		totalOK += s.SuccessCount
		if s.IsHealthy {
			healthyCount++
		}
	}

	// Compute RPS from delta
	now := time.Now()
	elapsed := now.Sub(c.lastSnapshotTime).Seconds()
	rps := 0.0
	if elapsed > 0 {
		delta := totalReq - c.lastTotalRequests
		rps = float64(delta) / elapsed
	}
	c.lastSnapshotTime = now
	c.lastTotalRequests = totalReq

	successRate := 0.0
	if totalReq > 0 {
		successRate = float64(totalOK) / float64(totalReq) * 100
	}

	// Copy circuit events
	events := make([]CircuitEvent, len(c.circuitEvents))
	copy(events, c.circuitEvents)

	return DashboardSnapshot{
		Servers:       snap,
		GlobalRPS:     rps,
		TotalRequests: totalReq,
		SuccessRate:   successRate,
		HealthyCount:  healthyCount,
		TotalCount:    len(c.servers),
		Algorithm:     c.algorithm,
		CircuitEvents: events,
	}
}

// CircuitEvents returns a copy of the circuit breaker event log.
func (c *Collector) CircuitEvents() []CircuitEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()
	events := make([]CircuitEvent, len(c.circuitEvents))
	copy(events, c.circuitEvents)
	return events
}
