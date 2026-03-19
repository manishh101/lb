package health

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"intelligent-lb/config"
	"intelligent-lb/internal/metrics"
)

// Monitor performs periodic health checks on all backend servers.
// Each server can have its own health check configuration (path, interval,
// timeout, expected status code), inspired by Traefik's per-service health checks.
type Monitor struct {
	servers  []config.ServerConfig
	metrics  *metrics.Collector
	breakers map[string]*Breaker
	stopChs  []chan struct{}
	mu       sync.Mutex
}

// NewMonitor creates a health monitor for the given servers.
// Each server runs its own independent health check goroutine with
// its own interval, timeout, and expected status code.
func NewMonitor(servers []config.ServerConfig, m *metrics.Collector,
	b map[string]*Breaker) *Monitor {
	return &Monitor{
		servers:  servers,
		metrics:  m,
		breakers: b,
	}
}

// Start launches per-server health check goroutines.
func (mon *Monitor) Start() {
	mon.mu.Lock()
	defer mon.mu.Unlock()

	for _, s := range mon.servers {
		stopCh := make(chan struct{})
		mon.stopChs = append(mon.stopChs, stopCh)

		server := s // capture loop variable
		go func() {
			interval := time.Duration(server.HealthCheck.IntervalSec) * time.Second
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			log.Printf("[MONITOR] Health checks started for %s (path=%s, interval=%ds, timeout=%ds, status=%d)",
				server.Name, server.HealthCheck.Path, server.HealthCheck.IntervalSec,
				server.HealthCheck.TimeoutSec, server.HealthCheck.ExpectedStatus)

			for {
				select {
				case <-ticker.C:
					mon.check(server)
				case <-stopCh:
					return
				}
			}
		}()
	}
}

// Stop halts all health check goroutines. Safe to call during hot reload.
func (mon *Monitor) Stop() {
	mon.mu.Lock()
	defer mon.mu.Unlock()

	for _, ch := range mon.stopChs {
		close(ch)
	}
	mon.stopChs = nil
	log.Println("[MONITOR] All health check goroutines stopped")
}

// check performs a single health check against a server using its per-server config.
func (mon *Monitor) check(server config.ServerConfig) {
	client := &http.Client{
		Timeout: time.Duration(server.HealthCheck.TimeoutSec) * time.Second,
	}

	url := fmt.Sprintf("%s%s", server.URL, server.HealthCheck.Path)
	resp, err := client.Get(url)

	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		mon.metrics.SetHealth(server.URL, false)
		mon.breakers[server.URL].RecordFailure()
		mon.metrics.SetCircuitState(server.URL, mon.breakers[server.URL].State())
		log.Printf("[MONITOR] %-20s %-10s DOWN ✗ (unreachable)", server.Name, server.URL)
		return
	}

	if resp.StatusCode != server.HealthCheck.ExpectedStatus {
		mon.metrics.SetHealth(server.URL, false)
		mon.breakers[server.URL].RecordFailure()
		mon.metrics.SetCircuitState(server.URL, mon.breakers[server.URL].State())
		log.Printf("[MONITOR] %-20s %-10s DOWN ✗ (status %d, expected %d)",
			server.Name, server.URL, resp.StatusCode, server.HealthCheck.ExpectedStatus)
		return
	}

	mon.metrics.SetHealth(server.URL, true)
	if mon.breakers[server.URL].RecordSuccess() {
		mon.metrics.ClearLatencies(server.URL)
	}
	mon.metrics.SetCircuitState(server.URL, mon.breakers[server.URL].State())
	log.Printf("[MONITOR] %-20s %-10s UP   ✓", server.Name, server.URL)
}
