// Package service manages named service instances for the load balancer.
// Each service has its own metrics collector, health monitor, circuit breakers,
// and load balancer instance. This is inspired by Traefik's service management
// in pkg/server/service/ where each service is independently managed.
package service

import (
	"log"
	"net/http"
	"sync"
	"time"

	"intelligent-lb/config"
	"intelligent-lb/internal/balancer"
	"intelligent-lb/internal/health"
	"intelligent-lb/internal/metrics"
	"intelligent-lb/internal/proxy"
)

// Instance holds all runtime components for a single named service.
// Each service manages its own goroutines for health checks, its own
// metrics collection, and its own circuit breakers — completely isolated
// from other services.
type Instance struct {
	Name      string
	Config    *config.ServiceConfig
	Collector *metrics.Collector
	Breakers  map[string]*health.Breaker
	Monitor   *health.Monitor
	Router    *balancer.Router
	Handler   http.Handler
}

// Manager manages the lifecycle of all named service instances.
// It provides access to service handlers for routing and aggregates
// metrics across all services for the dashboard.
type Manager struct {
	mu        sync.RWMutex
	instances map[string]*Instance
	cfg       *config.Config
}

// NewManager creates a new service manager from the given config.
// It builds a service instance for each entry in cfg.Services.
func NewManager(cfg *config.Config) *Manager {
	m := &Manager{
		instances: make(map[string]*Instance),
		cfg:       cfg,
	}
	m.buildAll(cfg)
	return m
}

// buildAll creates service instances for all configured services.
func (m *Manager) buildAll(cfg *config.Config) {
	for svcName, svcCfg := range cfg.Services {
		inst := m.buildInstance(svcName, svcCfg, cfg)
		m.instances[svcName] = inst
		log.Printf("[SERVICE] Created service %q: %d servers, algorithm=%s, canary=%v",
			svcName, len(svcCfg.Servers), svcCfg.LoadBalancer.Algorithm, svcCfg.Canary)
	}
}

// buildInstance creates a single service instance with its own metrics,
// breakers, health monitor, and proxy handler.
func (m *Manager) buildInstance(name string, svcCfg *config.ServiceConfig, cfg *config.Config) *Instance {
	var urls []string
	var names []string
	var weights []int

	for _, srv := range svcCfg.Servers {
		urls = append(urls, srv.URL)
		names = append(names, srv.Name)
		weights = append(weights, srv.Weight)
	}

	// Per-service metrics collector
	collector := metrics.New(urls, names, weights)
	algo := svcCfg.LoadBalancer.Algorithm
	if svcCfg.Canary {
		algo = "canary"
	}
	collector.SetAlgorithm(algo)

	// Per-service circuit breakers
	breakers := make(map[string]*health.Breaker)
	for _, url := range urls {
		breakers[url] = health.NewBreaker(
			svcCfg.CircuitBreaker.Threshold,
			time.Duration(svcCfg.CircuitBreaker.RecoveryTimeoutSec)*time.Second,
		)
	}

	// Per-service health monitor
	var monitor *health.Monitor
	if len(svcCfg.Servers) > 0 {
		monitor = health.NewMonitor(svcCfg.Servers, collector, breakers)
		monitor.Start()
	}

	// Per-service load balancer
	algorithm := getAlgorithm(algo)
	router := balancer.NewRouter(urls, collector, breakers, algorithm)

	// Per-service proxy handler
	handler := proxy.New(router, collector, breakers, cfg.MaxRetries, cfg.PerAttemptTimeoutSec)

	return &Instance{
		Name:      name,
		Config:    svcCfg,
		Collector: collector,
		Breakers:  breakers,
		Monitor:   monitor,
		Router:    router,
		Handler:   handler,
	}
}

// getAlgorithm returns the appropriate Algorithm implementation for the given name.
func getAlgorithm(name string) balancer.Algorithm {
	switch name {
	case "roundrobin":
		return &balancer.RoundRobin{}
	case "leastconn":
		return balancer.LeastConnections{}
	case "canary":
		return &balancer.Canary{}
	default:
		return balancer.WeightedScore{}
	}
}

// Get returns the HTTP handler for a named service, or nil if not found.
func (m *Manager) Get(name string) http.Handler {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inst, ok := m.instances[name]; ok {
		return inst.Handler
	}
	return nil
}

// GetInstance returns the service instance for a named service, or nil if not found.
func (m *Manager) GetInstance(name string) *Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.instances[name]
}

// Instances returns all service instances.
func (m *Manager) Instances() map[string]*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]*Instance, len(m.instances))
	for k, v := range m.instances {
		result[k] = v
	}
	return result
}

// GlobalCollector creates an aggregated collector across all services.
// This is used by the dashboard to show combined metrics.
func (m *Manager) GlobalCollector() *metrics.Collector {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var allURLs []string
	var allNames []string
	var allWeights []int
	seen := make(map[string]bool)

	for _, inst := range m.instances {
		for _, srv := range inst.Config.Servers {
			if !seen[srv.URL] {
				seen[srv.URL] = true
				allURLs = append(allURLs, srv.URL)
				allNames = append(allNames, srv.Name)
				allWeights = append(allWeights, srv.Weight)
			}
		}
	}

	collector := metrics.New(allURLs, allNames, allWeights)
	algo := m.cfg.Algorithm
	collector.SetAlgorithm(algo)
	return collector
}

// DashboardSnap builds a dashboard snapshot aggregated from all services.
// This implements the dashboard.SnapshotProvider interface.
func (m *Manager) DashboardSnap() metrics.DashboardSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	allServers := make(map[string]metrics.ServerStats)
	var allEvents []metrics.CircuitEvent
	totalRPS := 0.0

	for _, inst := range m.instances {
		snap := inst.Collector.DashboardSnap()
		for url, stats := range snap.Servers {
			allServers[url] = stats
		}
		allEvents = append(allEvents, snap.CircuitEvents...)
		totalRPS += snap.GlobalRPS
	}

	var totalReq, totalOK int64
	healthyCount := 0
	for _, s := range allServers {
		totalReq += s.TotalRequests
		totalOK += s.SuccessCount
		if s.IsHealthy {
			healthyCount++
		}
	}

	successRate := 0.0
	if totalReq > 0 {
		successRate = float64(totalOK) / float64(totalReq) * 100
	}

	// Trim events to last 50
	if len(allEvents) > 50 {
		allEvents = allEvents[len(allEvents)-50:]
	}

	return metrics.DashboardSnapshot{
		Servers:       allServers,
		GlobalRPS:     totalRPS,
		TotalRequests: totalReq,
		SuccessRate:   successRate,
		HealthyCount:  healthyCount,
		TotalCount:    len(allServers),
		Algorithm:     m.cfg.Algorithm,
		CircuitEvents: allEvents,
	}
}

// Stop stops all health monitors for all service instances.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, inst := range m.instances {
		if inst.Monitor != nil {
			inst.Monitor.Stop()
			log.Printf("[SERVICE] Stopped health monitor for service %q", name)
		}
	}
}
