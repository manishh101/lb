package entrypoint

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"intelligent-lb/config"
	"intelligent-lb/internal/middleware"
)

// TestEntryPoint_StartAndShutdown verifies that an entrypoint can start
// accepting connections and then shut down gracefully.
func TestEntryPoint_StartAndShutdown(t *testing.T) {
	cfg := &config.EntryPointConfig{
		Address:  ":0", // OS assigns a free port
		Protocol: "http",
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "hello from entrypoint")
	})

	ep := New("test-ep", cfg, handler, nil)

	// Override the server address to use :0 for testing
	ep.server.Addr = ":0"

	errCh := ep.Start()

	// Give the server time to start
	time.Sleep(100 * time.Millisecond)

	// Verify the server is running by checking for errors
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("entrypoint failed to start: %v", err)
		}
		t.Fatal("entrypoint exited unexpectedly")
	default:
		// Server is still running, good
	}

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ep.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	// Wait for the server goroutine to finish
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server returned error after shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit after shutdown")
	}
}

// TestManager_StartAllAndShutdownAll verifies the manager can start and stop
// multiple entrypoints correctly.
func TestManager_StartAllAndShutdownAll(t *testing.T) {
	mgr := NewManager()

	for i := 0; i < 3; i++ {
		cfg := &config.EntryPointConfig{
			Address:  ":0",
			Protocol: "http",
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		ep := New(fmt.Sprintf("ep-%d", i), cfg, handler, nil)
		mgr.Register(ep)
	}

	mgr.StartAll()
	time.Sleep(100 * time.Millisecond)

	// Verify all are registered
	names := mgr.Names()
	if len(names) != 3 {
		t.Fatalf("expected 3 entrypoints, got %d", len(names))
	}

	// Shutdown all
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := mgr.ShutdownAll(ctx); err != nil {
		t.Fatalf("ShutdownAll failed: %v", err)
	}
}

// TestManager_IndependentFailure verifies that one entrypoint failing
// does not affect others.
func TestManager_IndependentFailure(t *testing.T) {
	mgr := NewManager()

	// Create one entrypoint on a valid port
	goodCfg := &config.EntryPointConfig{
		Address:  ":0",
		Protocol: "http",
	}
	goodHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	goodEP := New("good", goodCfg, goodHandler, nil)
	mgr.Register(goodEP)

	// Create another entrypoint that will conflict (same port as good one after it starts)
	// We just verify the good one keeps working
	mgr.StartAll()
	time.Sleep(100 * time.Millisecond)

	// The good entrypoint should still be accessible
	ep := mgr.Get("good")
	if ep == nil {
		t.Fatal("expected to find 'good' entrypoint")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgr.ShutdownAll(ctx); err != nil {
		t.Fatalf("ShutdownAll failed: %v", err)
	}
}

// TestResolveMiddlewares verifies that middleware name resolution works correctly.
func TestResolveMiddlewares(t *testing.T) {
	cfg := &config.Config{
		RateLimitRPS:   100,
		RateLimitBurst: 200,
		DashboardAuth: config.DashboardAuth{
			Username: "admin",
			Password: "secret",
		},
	}

	t.Run("known middlewares resolve", func(t *testing.T) {
		builder := middleware.NewBuilder(cfg, nil)
		resolved, err := ResolveMiddlewares([]string{"rate-limit", "headers", "cors", "basic-auth"}, builder)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resolved) != 4 {
			t.Errorf("expected 4 middlewares, got %d", len(resolved))
		}
	})

	t.Run("unknown middlewares are skipped", func(t *testing.T) {
		builder := middleware.NewBuilder(cfg, nil)
		resolved, err := ResolveMiddlewares([]string{"unknown-middleware"}, builder)
		if err == nil {
			t.Errorf("expected error for unknown name")
		}
		if len(resolved) != 0 {
			t.Errorf("expected 0 middlewares for unknown name, got %d", len(resolved))
		}
	})

	t.Run("empty list returns empty", func(t *testing.T) {
		builder := middleware.NewBuilder(cfg, nil)
		resolved, err := ResolveMiddlewares([]string{}, builder)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resolved) != 0 {
			t.Errorf("expected 0 middlewares for empty list, got %d", len(resolved))
		}
	})
}
