package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"intelligent-lb/config"
	"intelligent-lb/internal/balancer"
	"intelligent-lb/internal/dashboard"
	"intelligent-lb/internal/health"
	"intelligent-lb/internal/metrics"
	"intelligent-lb/internal/proxy"
)

func main() {
	// Load configuration
	cfg, err := config.Load("config/config.json")
	if err != nil {
		log.Fatalf("[MAIN] Failed to load config: %v", err)
	}

	// Extract server URLs and names
	var serverURLs []string
	var serverNames []string
	for _, s := range cfg.Servers {
		serverURLs = append(serverURLs, s.URL)
		serverNames = append(serverNames, s.Name)
	}

	// Initialize metrics collector
	collector := metrics.New(serverURLs, serverNames)

	// Initialize circuit breakers (one per server)
	breakers := make(map[string]*health.Breaker)
	for _, url := range serverURLs {
		breakers[url] = health.NewBreaker(
			cfg.BreakerThreshold,
			time.Duration(cfg.BreakerTimeoutSec)*time.Second,
		)
	}

	// Initialize routing algorithm
	var algo balancer.Algorithm
	switch cfg.Algorithm {
	case "roundrobin":
		algo = &balancer.RoundRobin{}
	case "leastconn":
		algo = balancer.LeastConnections{}
	default:
		algo = balancer.WeightedScore{}
	}

	// Initialize router
	router := balancer.NewRouter(serverURLs, collector, breakers, algo)

	// Initialize health monitor
	monitor := health.NewMonitor(
		serverURLs, collector, breakers,
		time.Duration(cfg.HealthInterval)*time.Second,
	)
	monitor.Start()

	// Initialize proxy handler
	proxyHandler := proxy.New(router, collector, breakers)

	// Initialize dashboard
	hub := dashboard.NewHub(collector, "web/dashboard.html")
	hub.StartBroadcast()

	// Start terminal metrics reporter
	collector.StartReporter(cfg.MetricsIntervalSec)

	// Start dashboard server
	dashMux := http.NewServeMux()
	dashMux.HandleFunc("/", hub.ServeHTTP)
	dashMux.HandleFunc("/ws", hub.HandleWS)
	go func() {
		addr := fmt.Sprintf(":%d", cfg.DashboardPort)
		log.Printf("[MAIN] Dashboard server starting on %s", addr)
		if err := http.ListenAndServe(addr, dashMux); err != nil {
			log.Fatalf("[MAIN] Dashboard server failed: %v", err)
		}
	}()

	// Start load balancer proxy server
	addr := fmt.Sprintf(":%d", cfg.ListenPort)
	log.Printf("[MAIN] Load balancer starting on %s", addr)
	log.Printf("[MAIN] Algorithm: %s | Servers: %d | Health interval: %ds",
		cfg.Algorithm, len(serverURLs), cfg.HealthInterval)
	log.Printf("[MAIN] Dashboard: http://localhost:%d", cfg.DashboardPort)

	if err := http.ListenAndServe(addr, proxyHandler); err != nil {
		log.Fatalf("[MAIN] Load balancer failed: %v", err)
	}
}
