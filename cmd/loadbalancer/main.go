package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"intelligent-lb/config"
	"intelligent-lb/internal/balancer"
	"intelligent-lb/internal/dashboard"
	"intelligent-lb/internal/entrypoint"
	"intelligent-lb/internal/health"
	"intelligent-lb/internal/hotreload"
	"intelligent-lb/internal/logging"
	"intelligent-lb/internal/metrics"
	"intelligent-lb/internal/middleware"
	"intelligent-lb/internal/proxy"
	"intelligent-lb/internal/router"
	"intelligent-lb/internal/tlsutil"
)

// appState holds all mutable components that can be updated during hot reload.
type appState struct {
	mu        sync.RWMutex
	cfg       *config.Config
	collector *metrics.Collector
	breakers  map[string]*health.Breaker
	monitor   *health.Monitor

	// Legacy global proxy (when no routers match)
	proxy     *proxy.Handler

	// Rule-based routing
	routerMgr *router.Manager
	services  map[string]http.Handler
}

// ServeHTTP implements the top-level routing logic. It evaluates the request
// against rule-based routers. If a match is found, it forwards to the router's
// middleware-wrapped service handler. If no match (or no routers configured),
// it falls back to the legacy priority-based global proxy pool.
func (s *appState) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s.mu.RLock()
	route := s.routerMgr.Route(req)
	legacyProxy := s.proxy
	s.mu.RUnlock()

	if route != nil {
		route.Handler.ServeHTTP(w, req)
		return
	}
	legacyProxy.ServeHTTP(w, req)
}

func main() {
	// Load configuration
	cfg, err := config.Load("config/config.json")
	if err != nil {
		log.Fatalf("[MAIN] Failed to load config: %v", err)
	}

	// Initialize structured access log file
	if err := logging.InitFileLogger(cfg.AccessLogPath); err != nil {
		log.Printf("[MAIN] Warning: failed to initialize access log file: %v", err)
	}

	// Build the application state
	state := &appState{cfg: cfg}
	state.initialize(cfg)

	// Initialize dashboard
	hub := dashboard.NewHub(state.collector, "web/dashboard.html")
	hub.StartBroadcast()

	// Start terminal metrics reporter
	state.collector.StartReporter(cfg.MetricsIntervalSec)

	// ── Auto-generate TLS certs if needed ─────────────────────────────
	if cfg.TLS.Enabled && cfg.TLS.AutoGenerate {
		certFile := cfg.TLS.CertFile
		keyFile := cfg.TLS.KeyFile
		if certFile == "" {
			certFile = "server.crt"
		}
		if keyFile == "" {
			keyFile = "server.key"
		}
		if err := tlsutil.GenerateSelfSigned(certFile, keyFile); err != nil {
			log.Fatalf("[MAIN] Failed to generate self-signed certificate: %v", err)
		}
		log.Printf("[MAIN] Self-signed certificate generated: %s, %s", certFile, keyFile)
	}

	// ── Create Entrypoint Manager ─────────────────────────────────────
	epManager := entrypoint.NewManager()

	for epName, epCfg := range cfg.EntryPoints {
		// Resolve middleware names to actual middleware functions
		middlewares := entrypoint.ResolveMiddlewares(epCfg.Middlewares, cfg)

		var handler http.Handler

		if epName == "dashboard" {
			// Dashboard entrypoint gets the dashboard handler
			dashMux := http.NewServeMux()

			// Dashboard routes with the dashboard mux
			dashMux.Handle("/", http.HandlerFunc(hub.ServeHTTP))
			dashMux.Handle("/ws", http.HandlerFunc(hub.HandleWS))
			dashMux.Handle("/stats", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(state.collector.Snapshot()); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			}))

			handler = dashMux
		} else {
			// All other entrypoints get the top-level app state dispatcher
			handler = state
		}

		ep := entrypoint.New(epName, epCfg, handler, middlewares)
		epManager.Register(ep)
	}

	// ── Start All Entrypoints ─────────────────────────────────────────
	epManager.StartAll()

	// Print startup banner
	log.Printf("[MAIN] ═══════════════════════════════════════════════════")
	log.Printf("[MAIN] Intelligent Stateless Load Balancer")
	log.Printf("[MAIN] ═══════════════════════════════════════════════════")
	log.Printf("[MAIN] Algorithm:   %s", cfg.Algorithm)
	log.Printf("[MAIN] Servers:     %d (legacy), %d (services), %d (routers)", len(cfg.Servers), len(cfg.Services), len(cfg.Routers))
	log.Printf("[MAIN] Rate Limit:  %.0f rps/IP (burst %d)", cfg.RateLimitRPS, cfg.RateLimitBurst)
	log.Printf("[MAIN] Middlewares: %d configured", len(cfg.Middlewares))
	log.Printf("[MAIN] Entrypoints:")
	for name, ep := range cfg.EntryPoints {
		tlsStatus := "off"
		if ep.TLS != nil {
			tlsStatus = "enabled"
		}
		log.Printf("[MAIN]   %-12s → %s (protocol: %s, tls: %s, middlewares: %v)",
			name, ep.Address, ep.Protocol, tlsStatus, ep.Middlewares)
	}
	if cfg.HotReload {
		log.Printf("[MAIN] Hot Reload:  ENABLED")
	}
	log.Printf("[MAIN] ═══════════════════════════════════════════════════")

	// ── Config Hot Reload ──────────────────────────────────────────────
	if cfg.HotReload {
		_, err := hotreload.NewWatcher("config/config.json", func(path string) error {
			return state.reload(path)
		})
		if err != nil {
			log.Printf("[MAIN] Warning: hot reload failed to start: %v", err)
		}
	}

	// ── Graceful Shutdown ──────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	sig := <-quit
	log.Printf("[MAIN] Received signal: %v — initiating graceful shutdown...", sig)

	// 30-second timeout for graceful shutdown of all entrypoints
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := epManager.ShutdownAll(ctx); err != nil {
		log.Printf("[MAIN] Entrypoint shutdown errors: %v", err)
	}

	state.monitor.Stop()

	log.Println("[MAIN] Graceful shutdown complete. Goodbye!")
}

// initialize sets up all components from the given config.
func (s *appState) initialize(cfg *config.Config) {
	// 1. Gather all unique servers across global Servers and named Services
	allServers := make(map[string]config.ServerConfig)
	for _, srv := range cfg.Servers {
		allServers[srv.URL] = srv
	}
	for _, svc := range cfg.Services {
		for _, srv := range svc.Servers {
			allServers[srv.URL] = srv
		}
	}

	var serverURLs []string
	var serverNames []string
	var serverWeights []int
	var monitorServers []config.ServerConfig
	for url, srv := range allServers {
		serverURLs = append(serverURLs, url)
		serverNames = append(serverNames, srv.Name)
		serverWeights = append(serverWeights, srv.Weight)
		monitorServers = append(monitorServers, srv)
	}

	// 2. Initialize global metrics, breakers, and health monitor
	s.collector = metrics.New(serverURLs, serverNames, serverWeights)
	s.breakers = make(map[string]*health.Breaker)
	for _, url := range serverURLs {
		s.breakers[url] = health.NewBreaker(
			cfg.BreakerThreshold,
			time.Duration(cfg.BreakerTimeoutSec)*time.Second,
		)
	}
	
	// Only start monitor if we have servers to monitor
	if len(monitorServers) > 0 {
		s.monitor = health.NewMonitor(monitorServers, s.collector, s.breakers)
		s.monitor.Start()
	}

	// 3. Helper to create a balancer.Algorithm
	getAlgo := func(name string) balancer.Algorithm {
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

	// 4. Create legacy global proxy (for backward compatibility)
	var globalURLs []string
	for _, srv := range cfg.Servers {
		globalURLs = append(globalURLs, srv.URL)
	}
	globalRouter := balancer.NewRouter(globalURLs, s.collector, s.breakers, getAlgo(cfg.Algorithm))
	s.proxy = proxy.New(
		globalRouter, s.collector, s.breakers,
		cfg.MaxRetries, cfg.PerAttemptTimeoutSec,
	)

	// 5. Create specific proxy handlers for named Services
	s.services = make(map[string]http.Handler)
	for svcName, svcCfg := range cfg.Services {
		var svcURLs []string
		for _, srv := range svcCfg.Servers {
			svcURLs = append(svcURLs, srv.URL)
		}
		svcRouter := balancer.NewRouter(svcURLs, s.collector, s.breakers, getAlgo(cfg.Algorithm))
		s.services[svcName] = proxy.New(
			svcRouter, s.collector, s.breakers,
			cfg.MaxRetries, cfg.PerAttemptTimeoutSec,
		)
	}

	// 6. Build the rule-based router manager
	s.routerMgr = router.NewManager()
	for rtName, rtCfg := range cfg.Routers {
		// Resolve the target service
		svcHandler, ok := s.services[rtCfg.Service]
		if !ok {
			log.Printf("[MAIN] Warning: router %q references unknown service %q", rtName, rtCfg.Service)
			continue
		}

		// Resolve router-specific middlewares using the config-driven builder
		middlewares := entrypoint.ResolveMiddlewares(rtCfg.Middlewares, cfg)
		
		// Wrap the service handler with the router's middlewares
		finalHandler := svcHandler
		if len(middlewares) > 0 {
			chain := middleware.Chain(middlewares...)
			finalHandler = chain(finalHandler)
		}

		if err := s.routerMgr.AddRoute(rtName, rtCfg.Rule, rtCfg.Priority, rtCfg.Middlewares, rtCfg.Service, finalHandler); err != nil {
			log.Printf("[MAIN] Warning: failed to add router %q: %v", rtName, err)
		}
	}
}

// reload re-reads the config and swaps out mutable components.
func (s *appState) reload(path string) error {
	newCfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.monitor != nil {
		s.monitor.Stop()
	}

	s.cfg = newCfg
	s.initialize(newCfg)

	log.Printf("[MAIN] Hot reload complete: %d legacy servers, %d routers, %d middlewares",
		len(newCfg.Servers), len(newCfg.Routers), len(newCfg.Middlewares))
	return nil
}
