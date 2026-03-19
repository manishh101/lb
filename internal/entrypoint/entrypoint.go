package entrypoint

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"intelligent-lb/config"
	"intelligent-lb/internal/middleware"
)

// EntryPoint represents a single named entrypoint that runs as its own
// independent HTTP server with its own goroutine, middleware chain, and
// connection handling. This is inspired by Traefik's TCPEntryPoint struct
// in pkg/server/server_entrypoint_tcp.go.
type EntryPoint struct {
	Name    string
	Config  *config.EntryPointConfig
	server  *http.Server
	handler http.Handler
}

// New creates a new EntryPoint with the given name, config, base handler,
// and middleware chain applied. The middlewares wrap the base handler so
// that every request coming through this entrypoint passes through them
// before reaching the actual handler.
func New(name string, cfg *config.EntryPointConfig, handler http.Handler, middlewares []middleware.Middleware) *EntryPoint {
	// Apply the middleware chain to the handler.
	// Middlewares execute left-to-right: Chain(A, B, C)(handler)
	// produces A(B(C(handler))), so the request flows A → B → C → handler.
	if len(middlewares) > 0 {
		chain := middleware.Chain(middlewares...)
		handler = chain(handler)
	}

	return &EntryPoint{
		Name:    name,
		Config:  cfg,
		handler: handler,
		server: &http.Server{
			Addr:         cfg.Address,
			Handler:      handler,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
	}
}

// Start begins serving on the entrypoint's address in a goroutine.
// For HTTPS entrypoints, it uses ListenAndServeTLS with the configured cert/key.
// This mirrors Traefik's TCPEntryPoint.Start() which runs each entrypoint
// in its own goroutine with independent connection handling.
//
// The returned channel receives an error if the server fails to start,
// or nil when the server shuts down normally.
func (ep *EntryPoint) Start() <-chan error {
	errCh := make(chan error, 1)

	go func() {
		log.Printf("[ENTRYPOINT] %s starting on %s (protocol: %s)", ep.Name, ep.Config.Address, ep.Config.Protocol)

		var err error
		if ep.Config.Protocol == "https" && ep.Config.TLS != nil {
			certFile := ep.Config.TLS.CertFile
			keyFile := ep.Config.TLS.KeyFile
			if certFile == "" {
				certFile = "server.crt"
			}
			if keyFile == "" {
				keyFile = "server.key"
			}
			err = ep.server.ListenAndServeTLS(certFile, keyFile)
		} else {
			err = ep.server.ListenAndServe()
		}

		if err != nil && err != http.ErrServerClosed {
			log.Printf("[ENTRYPOINT] %s failed: %v", ep.Name, err)
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	return errCh
}

// Shutdown gracefully shuts down the entrypoint's HTTP server.
// It stops accepting new connections and waits for in-flight requests
// to complete, using the provided context for timeout control.
// This mirrors Traefik's TCPEntryPoint.Shutdown() method.
func (ep *EntryPoint) Shutdown(ctx context.Context) error {
	log.Printf("[ENTRYPOINT] %s shutting down...", ep.Name)
	if err := ep.server.Shutdown(ctx); err != nil {
		log.Printf("[ENTRYPOINT] %s forced shutdown: %v", ep.Name, err)
		return err
	}
	log.Printf("[ENTRYPOINT] %s shutdown complete", ep.Name)
	return nil
}

// Manager manages the lifecycle of multiple named entrypoints.
// This is directly inspired by Traefik's TCPEntryPoints map type,
// which holds a map[string]*TCPEntryPoint and provides Start()/Stop()
// methods to manage all entrypoints collectively.
type Manager struct {
	mu          sync.RWMutex
	entrypoints map[string]*EntryPoint
	errChans    map[string]<-chan error
}

// NewManager creates a new entrypoint manager.
func NewManager() *Manager {
	return &Manager{
		entrypoints: make(map[string]*EntryPoint),
		errChans:    make(map[string]<-chan error),
	}
}

// Register adds an entrypoint to the manager.
func (m *Manager) Register(ep *EntryPoint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entrypoints[ep.Name] = ep
}

// Get returns an entrypoint by name, or nil if not found.
func (m *Manager) Get(name string) *EntryPoint {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.entrypoints[name]
}

// Names returns a sorted list of all registered entrypoint names.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.entrypoints))
	for name := range m.entrypoints {
		names = append(names, name)
	}
	return names
}

// StartAll starts all registered entrypoints in separate goroutines.
// Each entrypoint runs independently — failure of one does not affect others.
// This mirrors Traefik's TCPEntryPoints.Start() which iterates over all
// entrypoints and starts each in its own goroutine.
func (m *Manager) StartAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, ep := range m.entrypoints {
		m.errChans[name] = ep.Start()
	}

	log.Printf("[ENTRYPOINT] Started %d entrypoints", len(m.entrypoints))
}

// ShutdownAll gracefully shuts down all entrypoints concurrently.
// It uses a WaitGroup to wait for all shutdowns to complete, collecting
// any errors. This directly mirrors Traefik's TCPEntryPoints.Stop()
// which uses wg.Go() for concurrent shutdown of all entrypoints.
func (m *Manager) ShutdownAll(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for name, ep := range m.entrypoints {
		wg.Add(1)
		go func(name string, ep *EntryPoint) {
			defer wg.Done()
			if err := ep.Shutdown(ctx); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("entrypoint %s: %w", name, err))
				mu.Unlock()
			}
		}(name, ep)
	}

	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}

	log.Printf("[ENTRYPOINT] All entrypoints shut down successfully")
	return nil
}

// ResolveMiddlewares resolves a list of middleware names to actual middleware
// functions. It uses the config-driven Builder when a middlewares block exists,
// falling back to legacy name-based resolution.
//
// This is the primary entry point for middleware resolution from entrypoints
// and routers — called during startup and hot reload.
func ResolveMiddlewares(names []string, cfg *config.Config) []middleware.Middleware {
	builder := middleware.NewBuilder(cfg)
	var resolved []middleware.Middleware

	for _, name := range names {
		mw, err := builder.Build(name)
		if err != nil {
			log.Printf("[ENTRYPOINT] Warning: failed to build middleware %q: %v", name, err)
			continue
		}
		resolved = append(resolved, mw)
	}

	return resolved
}
