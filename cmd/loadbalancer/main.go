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
	"intelligent-lb/internal/dashboard"
	"intelligent-lb/internal/entrypoint"
	"intelligent-lb/internal/hotreload"
	"intelligent-lb/internal/logging"
	"intelligent-lb/internal/middleware"
	"intelligent-lb/internal/router"
	"intelligent-lb/internal/service"
	"intelligent-lb/internal/tlsutil"
)

// appState holds all mutable components that can be updated during hot reload.
type appState struct {
	mu        sync.RWMutex
	cfg       *config.Config
	svcMgr    *service.Manager

	// Rule-based routing
	routerMgr *router.Manager
}

// ServeHTTP implements the top-level routing logic. It evaluates the request
// against rule-based routers. If a match is found, it forwards to the router's
// middleware-wrapped service handler. If no match (or no routers configured),
// it falls back to the "default" service.
func (s *appState) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s.mu.RLock()
	route := s.routerMgr.Route(req)
	svcMgr := s.svcMgr
	s.mu.RUnlock()

	if route != nil {
		route.Handler.ServeHTTP(w, req)
		return
	}

	// Fallback to "default" service (backward compatibility for flat server list)
	defaultHandler := svcMgr.Get("default")
	if defaultHandler != nil {
		defaultHandler.ServeHTTP(w, req)
		return
	}

	// If no default service and no route match, return 502
	http.Error(w, "No matching service", http.StatusBadGateway)
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

	// Build the application state using service manager
	state := &appState{cfg: cfg}
	if err := state.initialize(cfg); err != nil {
		log.Fatalf("[MAIN] Failed to initialize state: %v", err)
	}

	// Initialize dashboard using the service manager as SnapshotProvider.
	// The service manager aggregates metrics from all service instances.
	hub := dashboard.NewHub(state.svcMgr, "web/dashboard.html")
	hub.StartBroadcast()

	// Start terminal metrics reporter using any available service's collector
	for _, inst := range state.svcMgr.Instances() {
		inst.Collector.StartReporter(cfg.MetricsIntervalSec)
		break // just start one reporter for terminal output
	}

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
	mwBuilder := middleware.NewBuilder(cfg, state.svcMgr)

	for epName, epCfg := range cfg.EntryPoints {
		middlewares, err := entrypoint.ResolveMiddlewares(epCfg.Middlewares, mwBuilder)
		if err != nil {
			log.Fatalf("[MAIN] Failed to resolve middlewares for entrypoint %q: %v", epName, err)
		}
		var handler http.Handler

		if epName == "dashboard" {
			dashMux := http.NewServeMux()
			dashMux.Handle("/", http.HandlerFunc(hub.ServeHTTP))
			dashMux.Handle("/ws", http.HandlerFunc(hub.HandleWS))
			dashMux.Handle("/api/metrics", http.HandlerFunc(hub.HandleAPIMetrics))
			dashMux.Handle("/api/history", http.HandlerFunc(hub.HandleAPIHistory))
			dashMux.Handle("/api/health", http.HandlerFunc(hub.HandleAPIHealth))
			dashMux.Handle("/stats", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				snap := state.svcMgr.DashboardSnap()
				if err := json.NewEncoder(w).Encode(snap); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			}))
			handler = dashMux
		} else {
			handler = state
		}

		ep := entrypoint.New(epName, epCfg, handler, middlewares)
		epManager.Register(ep)
	}

	// ── Start All Entrypoints ─────────────────────────────────────────
	epManager.StartAll()

	// ── Startup Banner ────────────────────────────────────────────────
	log.Printf("[MAIN] ═══════════════════════════════════════════════════")
	log.Printf("[MAIN] Intelligent Stateless Load Balancer")
	log.Printf("[MAIN] ═══════════════════════════════════════════════════")
	log.Printf("[MAIN] Algorithm:   %s", cfg.Algorithm)
	svcCount := 0
	serverCount := 0
	for _, svc := range cfg.Services {
		svcCount++
		serverCount += len(svc.Servers)
	}
	log.Printf("[MAIN] Services:    %d (%d total servers), %d routers", svcCount, serverCount, len(cfg.Routers))
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
	for svcName, svcCfg := range cfg.Services {
		var serverNames []string
		for _, srv := range svcCfg.Servers {
			serverNames = append(serverNames, srv.Name)
		}
		algo := svcCfg.LoadBalancer.Algorithm
		if svcCfg.Canary {
			algo = "canary"
		}
		log.Printf("[MAIN] Service %-12s: algorithm=%s, canary=%v, health=%s/%ds, breaker=%d/%ds, servers=%v",
			svcName, algo, svcCfg.Canary,
			svcCfg.HealthCheck.Path, svcCfg.HealthCheck.IntervalSec,
			svcCfg.CircuitBreaker.Threshold, svcCfg.CircuitBreaker.RecoveryTimeoutSec,
			serverNames)
	}
	if cfg.HotReload {
		log.Printf("[MAIN] Hot Reload:  ENABLED")
	}
	log.Printf("[MAIN] REST API:    /api/metrics, /api/history, /api/health")
	log.Printf("[MAIN] ═══════════════════════════════════════════════════")

	// ── Config Hot Reload ──────────────────────────────────────────────
	if cfg.HotReload {
		_, err := hotreload.NewWatcher("config/config.json", func(path string) error {
			return state.reload(path, hub)
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := epManager.ShutdownAll(ctx); err != nil {
		log.Printf("[MAIN] Entrypoint shutdown errors: %v", err)
	}

	state.svcMgr.Stop()
	log.Println("[MAIN] Graceful shutdown complete. Goodbye!")
}

// initialize sets up all components from the given config.
func (s *appState) initialize(cfg *config.Config) error {
	// Create service manager — each service gets its own metrics, health, breakers, balancer
	s.svcMgr = service.NewManager(cfg)

	// Build the rule-based router manager
	s.routerMgr = router.NewManager()
	mwBuilder := middleware.NewBuilder(cfg, s.svcMgr)

	for rtName, rtCfg := range cfg.Routers {
		svcHandler := s.svcMgr.Get(rtCfg.Service)
		if svcHandler == nil {
			return fmt.Errorf("router %q references unknown service %q", rtName, rtCfg.Service)
		}

		middlewares, err := entrypoint.ResolveMiddlewares(rtCfg.Middlewares, mwBuilder)
		if err != nil {
			return fmt.Errorf("router %q middleware resolution failed: %w", rtName, err)
		}

		finalHandler := svcHandler
		if len(middlewares) > 0 {
			chain := middleware.Chain(middlewares...)
			finalHandler = chain(finalHandler)
		}

		if err := s.routerMgr.AddRoute(rtName, rtCfg.Rule, rtCfg.Priority, rtCfg.Middlewares, rtCfg.Service, finalHandler); err != nil {
			return fmt.Errorf("failed to add router %q: %w", rtName, err)
		}
	}
	return nil
}

// reload re-reads the config, logs changes, and swaps out mutable components.
func (s *appState) reload(path string, hub *dashboard.Hub) error {
	newCfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oldCfg := s.cfg

	// Stop existing service monitors
	s.svcMgr.Stop()
	oldSvcMgr := s.svcMgr

	// Log what changed
	logConfigDiff(oldCfg, newCfg)

	// Rebuild everything from the new config
	s.cfg = newCfg
	if err := s.initialize(newCfg); err != nil {
		log.Printf("[HOTRELOAD] Error initializing new config: %v (rolling back configuration may be needed)", err)
		// We already stopped the old manager! A true transactional reload would build the new state first.
		// For now, return the error.
		return err
	}

	// Migrate metrics from old to new
	s.svcMgr.ImportMetrics(oldSvcMgr)

	// Update dashboard hub with new service manager as provider
	hub.SetProvider(s.svcMgr)

	log.Printf("[MAIN] Hot reload complete: %d services, %d routers, %d middlewares",
		len(newCfg.Services), len(newCfg.Routers), len(newCfg.Middlewares))
	return nil
}

// logConfigDiff logs detailed differences between old and new configs.
func logConfigDiff(old, new *config.Config) {
	for name, newSvc := range new.Services {
		oldSvc, existed := old.Services[name]
		if !existed {
			log.Printf("[HOTRELOAD] + Added service %q with %d servers", name, len(newSvc.Servers))
			continue
		}
		oldURLs := make(map[string]config.ServerConfig)
		for _, srv := range oldSvc.Servers {
			oldURLs[srv.URL] = srv
		}
		newURLs := make(map[string]config.ServerConfig)
		for _, srv := range newSvc.Servers {
			newURLs[srv.URL] = srv
		}
		for url, srv := range newURLs {
			if _, ok := oldURLs[url]; !ok {
				log.Printf("[HOTRELOAD] + Service %q: added server %s (%s)", name, srv.Name, url)
			} else if oldURLs[url].Weight != srv.Weight {
				log.Printf("[HOTRELOAD] ~ Service %q: server %s weight changed %d → %d",
					name, srv.Name, oldURLs[url].Weight, srv.Weight)
			}
		}
		for url, srv := range oldURLs {
			if _, ok := newURLs[url]; !ok {
				log.Printf("[HOTRELOAD] - Service %q: removed server %s (%s)", name, srv.Name, url)
			}
		}
		// Check health check changes
		if oldSvc.HealthCheck != nil && newSvc.HealthCheck != nil {
			if oldSvc.HealthCheck.IntervalSec != newSvc.HealthCheck.IntervalSec {
				log.Printf("[HOTRELOAD] ~ Service %q: health interval changed %d → %d",
					name, oldSvc.HealthCheck.IntervalSec, newSvc.HealthCheck.IntervalSec)
			}
		}
		// Check canary changes
		if oldSvc.Canary != newSvc.Canary {
			log.Printf("[HOTRELOAD] ~ Service %q: canary changed %v → %v", name, oldSvc.Canary, newSvc.Canary)
		}
	}
	for name := range old.Services {
		if _, ok := new.Services[name]; !ok {
			log.Printf("[HOTRELOAD] - Removed service %q", name)
		}
	}
}
