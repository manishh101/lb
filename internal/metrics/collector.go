package metrics

import (
	"sync"
	"time"
)

// ServerStats holds all metrics for a single backend server.
type ServerStats struct {
	Name              string  `json:"name"`
	URL               string  `json:"url"`
	IsHealthy         bool    `json:"is_healthy"`
	AvgLatencyMs      float64 `json:"avg_latency_ms"`
	ActiveConnections int64   `json:"active_connections"`
	TotalRequests     int64   `json:"total_requests"`
	SuccessCount      int64   `json:"success_count"`
	FailureCount      int64   `json:"failure_count"`
	CircuitState      string  `json:"circuit_state"`
	HighPriorityCount int64   `json:"high_priority_count"`
	LowPriorityCount  int64   `json:"low_priority_count"`
	LastChecked       string  `json:"last_checked"`
	recentLatencies   []float64
}

// Collector is a thread-safe metrics store for all backend servers.
type Collector struct {
	mu      sync.RWMutex
	servers map[string]*ServerStats
}

// New creates a Collector and initializes metrics for each server.
func New(servers []string, names []string) *Collector {
	c := &Collector{servers: make(map[string]*ServerStats)}
	for i, url := range servers {
		name := url
		if i < len(names) {
			name = names[i]
		}
		c.servers[url] = &ServerStats{
			Name:         name,
			URL:          url,
			IsHealthy:    true,
			CircuitState: "CLOSED",
		}
	}
	return c
}

// RecordStart increments the active connection count for a server.
func (c *Collector) RecordStart(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.servers[url]; ok {
		s.ActiveConnections++
	}
}

// RecordEnd decrements active connections, records latency, and updates success/failure.
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

	// Rolling latency window (max 50 samples)
	s.recentLatencies = append(s.recentLatencies, latencyMs)
	if len(s.recentLatencies) > 50 {
		s.recentLatencies = s.recentLatencies[1:]
	}
	sum := 0.0
	for _, v := range s.recentLatencies {
		sum += v
	}
	s.AvgLatencyMs = sum / float64(len(s.recentLatencies))

	if success {
		s.SuccessCount++
	} else {
		s.FailureCount++
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

// SetCircuitState updates the circuit breaker state for a server.
func (c *Collector) SetCircuitState(url string, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.servers[url]; ok {
		s.CircuitState = state
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
