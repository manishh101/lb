package health

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"intelligent-lb/internal/metrics"
)

// Monitor performs periodic health checks on all backend servers.
// If a server fails, it updates metrics and the circuit breaker.
type Monitor struct {
	servers  []string
	metrics  *metrics.Collector
	breakers map[string]*Breaker
	interval time.Duration
	client   *http.Client
}

// NewMonitor creates a health monitor for the given servers.
func NewMonitor(servers []string, m *metrics.Collector,
	b map[string]*Breaker, interval time.Duration) *Monitor {
	return &Monitor{
		servers:  servers,
		metrics:  m,
		breakers: b,
		interval: interval,
		client:   &http.Client{Timeout: 2 * time.Second},
	}
}

// Start launches the background health check goroutine.
func (mon *Monitor) Start() {
	go func() {
		ticker := time.NewTicker(mon.interval)
		log.Println("[MONITOR] Health checks started")
		for range ticker.C {
			for _, url := range mon.servers {
				go mon.check(url)
			}
		}
	}()
}

// check performs a single health check against a server's /health endpoint.
func (mon *Monitor) check(url string) {
	resp, err := mon.client.Get(fmt.Sprintf("%s/health", url))

	// FIX B3: Guard against nil resp before calling Body.Close()
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil || resp.StatusCode != http.StatusOK {
		mon.metrics.SetHealth(url, false)
		mon.breakers[url].RecordFailure()
		mon.metrics.SetCircuitState(url, mon.breakers[url].State())
		log.Printf("[MONITOR] %-30s DOWN ✗", url)
		return
	}

	mon.metrics.SetHealth(url, true)
	mon.breakers[url].RecordSuccess()
	mon.metrics.SetCircuitState(url, mon.breakers[url].State())
	log.Printf("[MONITOR] %-30s UP   ✓", url)
}
