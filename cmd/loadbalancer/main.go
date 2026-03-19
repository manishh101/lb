package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"intelligent-lb/config"
	"intelligent-lb/internal/balancer"
	"intelligent-lb/internal/dashboard"
	"intelligent-lb/internal/health"
	"intelligent-lb/internal/metrics"
	"intelligent-lb/internal/proxy"
	"intelligent-lb/internal/ratelimiter"
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
	var serverWeights []int
	for _, s := range cfg.Servers {
		serverURLs = append(serverURLs, s.URL)
		serverNames = append(serverNames, s.Name)
		serverWeights = append(serverWeights, s.Weight)
	}

	// Initialize metrics collector
	collector := metrics.New(serverURLs, serverNames, serverWeights)

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
	proxyHandler := proxy.New(router, collector, breakers, cfg.MaxRetries, cfg.PerAttemptTimeoutSec)
	
	// Wrap proxy handler with Rate Limiter
	rl := ratelimiter.New(cfg.RateLimitRPS, cfg.RateLimitBurst)
	rateLimitedProxy := rl.Middleware(proxyHandler)

	// Initialize dashboard
	hub := dashboard.NewHub(collector, "web/dashboard.html")
	hub.StartBroadcast()

	// Start terminal metrics reporter
	collector.StartReporter(cfg.MetricsIntervalSec)

	// ── Dashboard Server (background) ──────────────────────────────────
	dashMux := http.NewServeMux()
	dashMux.HandleFunc("/", hub.ServeHTTP)
	dashMux.HandleFunc("/ws", hub.HandleWS)
	dashMux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(collector.Snapshot()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	dashAddr := fmt.Sprintf(":%d", cfg.DashboardPort)
	dashServer := &http.Server{
		Addr:    dashAddr,
		Handler: dashMux,
	}
	go func() {
		log.Printf("[MAIN] Dashboard server starting on %s", dashAddr)
		if err := dashServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[MAIN] Dashboard server failed: %v", err)
		}
	}()

	// ── Load Balancer Proxy Server ─────────────────────────────────────
	lbAddr := fmt.Sprintf(":%d", cfg.ListenPort)
	lbServer := &http.Server{
		Addr:         lbAddr,
		Handler:      rateLimitedProxy,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[MAIN] ═══════════════════════════════════════════════════")
		log.Printf("[MAIN] Intelligent Stateless Load Balancer")
		log.Printf("[MAIN] ═══════════════════════════════════════════════════")
		log.Printf("[MAIN] Listen:     %s", lbAddr)
		log.Printf("[MAIN] Algorithm:  %s", cfg.Algorithm)
		log.Printf("[MAIN] Servers:    %d", len(serverURLs))
		log.Printf("[MAIN] Health:     every %ds", cfg.HealthInterval)
		log.Printf("[MAIN] Dashboard:  http://localhost:%d", cfg.DashboardPort)
		log.Printf("[MAIN] ═══════════════════════════════════════════════════")
		if err := lbServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[MAIN] Load balancer failed: %v", err)
		}
	}()

	// ── Graceful Shutdown ──────────────────────────────────────────────
	// Trap SIGINT (Ctrl+C) and SIGTERM (Docker/K8s stop signal)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	sig := <-quit
	log.Printf("[MAIN] Received signal: %v — initiating graceful shutdown...", sig)

	// Allow up to 15 seconds for in-flight requests to complete
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSec)*time.Second)
	defer cancel()

	// Shutdown LB server first (stop accepting new requests, drain existing)
	if err := lbServer.Shutdown(ctx); err != nil {
		log.Printf("[MAIN] LB server forced shutdown: %v", err)
	}

	// Then shutdown dashboard
	if err := dashServer.Shutdown(ctx); err != nil {
		log.Printf("[MAIN] Dashboard server forced shutdown: %v", err)
	}

	log.Println("[MAIN] Graceful shutdown complete. Goodbye!")
}
