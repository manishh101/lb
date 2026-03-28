# `cmd/loadbalancer/` — Application Entry Point

**Go package:** `main`

This is the **main program** for the Intelligent Load Balancer. It wires together all the `internal/` packages, sets up entrypoints, starts the dashboard, configures hot reload, and manages graceful shutdown. It is the only `package main` that produces the load balancer binary.

---

## File Structure

```
cmd/
└── loadbalancer/
    └── main.go  — main(), appState, initialize(), reload(), logConfigDiff()
```

---

## Build and Run

```bash
# Run directly:
go run ./cmd/loadbalancer/

# Build binary:
go build -o bin/lb ./cmd/loadbalancer/

# Run compiled binary:
./bin/lb
```

The binary always reads configuration from `config/config.json` relative to the working directory.

---

## `appState` — Mutable Runtime State

```go
type appState struct {
    mu        sync.RWMutex
    cfg       *config.Config
    svcMgr    *service.Manager
    routerMgr *router.Manager
}
```

Holds all components that are **swapped out** during a hot reload. Protected by `sync.RWMutex`:
- Multiple goroutines (incoming requests) hold a read lock during `ServeHTTP`.
- The hot reload callback holds a write lock during atomic swap.

### `ServeHTTP(w, req)` — Top-Level Router

`appState` implements `http.Handler`. This is the handler for all non-dashboard entrypoints:

```go
func (s *appState) ServeHTTP(w http.ResponseWriter, req *http.Request) {
    s.mu.RLock()
    route := s.routerMgr.Route(req)           // rule-based route match
    svcMgr := s.svcMgr                        // capture under lock
    s.mu.RUnlock()

    if route != nil {
        route.Handler.ServeHTTP(w, req)        // → route's middleware chain → service
        return
    }

    // Fallback to "default" service (backward compatibility)
    if defaultHandler := svcMgr.Get("default"); defaultHandler != nil {
        defaultHandler.ServeHTTP(w, req)
        return
    }

    http.Error(w, "No matching service", http.StatusBadGateway)
}
```

---

## `main()` — Startup Sequence

The startup sequence in `main()` follows a deterministic order:

### 1. Load Configuration
```go
cfg, err := config.Load("config/config.json")
```
Parses JSON, applies defaults, runs validation. Fatals if config is invalid.

### 2. Initialize Access Log File
```go
logging.InitFileLogger(cfg.AccessLogPath)
```
Opens `./logs/access.log` (or configured path) in append mode.

### 3. Initialize App State
```go
state := &appState{cfg: cfg}
state.initialize(cfg)
```
Calls `initialize()` which builds `service.Manager` and `router.Manager`.

### 4. Start Dashboard Hub
```go
hub := dashboard.NewHub(state.svcMgr, "web/dashboard.html")
hub.StartBroadcast()
```
Starts the 1-second WebSocket broadcast goroutine. The `svcMgr` is the `SnapshotProvider`.

### 5. Start Terminal Reporter
```go
for _, inst := range state.svcMgr.Instances() {
    inst.Collector.StartReporter(cfg.MetricsIntervalSec)
    break  // only one reporter, for any service
}
```
Prints the ASCII metrics table every `metrics_interval_sec` seconds.

### 6. TLS Certificate Auto-Generation
```go
if cfg.TLS.Enabled && cfg.TLS.AutoGenerate {
    tlsutil.GenerateSelfSigned(certFile, keyFile)
}
```
Only runs if TLS is both enabled and `auto_generate: true`.

### 7. Build and Register Entrypoints
```go
epManager := entrypoint.NewManager()
mwBuilder := middleware.NewBuilder(cfg, state.svcMgr)

for epName, epCfg := range cfg.EntryPoints {
    middlewares, _ := entrypoint.ResolveMiddlewares(epCfg.Middlewares, mwBuilder)
    var handler http.Handler

    if epName == "dashboard" {
        // Dashboard gets its own ServeMux with specific API routes
        dashMux := http.NewServeMux()
        dashMux.Handle("/",            hub.ServeHTTP)
        dashMux.Handle("/ws",          hub.HandleWS)
        dashMux.Handle("/api/metrics", hub.HandleAPIMetrics)
        dashMux.Handle("/api/history", hub.HandleAPIHistory)
        dashMux.Handle("/api/health",  hub.HandleAPIHealth)
        dashMux.Handle("/api/config",  hub.HandleAPIConfig)
        dashMux.Handle("/stats",       ...)   // JSON snapshot
        handler = dashMux
    } else {
        handler = state  // uses ServeHTTP above
    }

    ep := entrypoint.New(epName, epCfg, handler, middlewares)
    epManager.Register(ep)
}
epManager.StartAll()
```

### 8. Start Config Hot Reload
```go
if cfg.HotReload {
    hotreload.NewWatcher("config/config.json", func(path string) error {
        return state.reload(path, hub)
    })
}
```

### 9. Wait for OS Signal + Graceful Shutdown
```go
quit := make(chan os.Signal, 1)
signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
sig := <-quit

ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
epManager.ShutdownAll(ctx)  // stops accepting new connections, waits for in-flight
state.svcMgr.Stop()         // stops health check goroutines
```

---

## `initialize(cfg *config.Config) error`

Called at startup and on every hot reload. Builds:
1. `service.Manager` — creates per-service instances (metrics, breakers, health, proxy).
2. `router.Manager` — registers routes:
   - For each router in `cfg.Routers`: resolves the service handler, applies route-level middleware chain, calls `routerMgr.AddRoute()`.
   - Returns error if a router references an unknown service name.

---

## `reload(path string, hub *dashboard.Hub) error`

The hot reload callback. Called with a write lock held by `appState`:

```go
// 1. Parse new config
newCfg, _ := config.Load(path)

s.mu.Lock()
defer s.mu.Unlock()

// 2. Stop old health goroutines
s.svcMgr.Stop()
oldSvcMgr := s.svcMgr

// 3. Diff and log changes
logConfigDiff(s.cfg, newCfg)

// 4. Rebuild everything
s.cfg = newCfg
s.initialize(newCfg)

// 5. Migrate metrics from old to new
s.svcMgr.ImportMetrics(oldSvcMgr)

// 6. Update dashboard provider
hub.SetProvider(s.svcMgr)
```

**Note:** The current implementation stops the old manager before building the new one. A production-grade implementation would build the new state first and only stop the old one after successful initialization.

---

## `logConfigDiff(old, new *config.Config)`

Logs detailed change events for hot reload:

| Change | Log Prefix | Example |
|---|---|---|
| New service | `[HOTRELOAD] +` | `+ Added service "api-v2" with 2 servers` |
| Removed service | `[HOTRELOAD] -` | `- Removed service "api-v1"` |
| New server | `[HOTRELOAD] +` | `+ Service "api": added server Alpha (http://...)` |
| Removed server | `[HOTRELOAD] -` | `- Service "api": removed server Beta (http://...)` |
| Weight change | `[HOTRELOAD] ~` | `~ Server Alpha weight changed 3 → 5` |
| Health interval change | `[HOTRELOAD] ~` | `~ Service api: health interval changed 5 → 10` |
| Canary toggle | `[HOTRELOAD] ~` | `~ Service api: canary changed false → true` |

---

## Startup Banner

On startup, main logs a comprehensive status banner:

```
[MAIN] ═══════════════════════════════════════════════════
[MAIN] Intelligent Stateless Load Balancer
[MAIN] ═══════════════════════════════════════════════════
[MAIN] Algorithm:   weighted
[MAIN] Services:    3 (4 total servers), 3 routers
[MAIN] Rate Limit:  100 rps/IP (burst 200)
[MAIN] Middlewares: 8 configured
[MAIN] Entrypoints:
[MAIN]   web          → :8082 (protocol: http, tls: off, middlewares: [...])
[MAIN]   dashboard    → :8081 (protocol: http, tls: off, middlewares: [...])
[MAIN] Service fast-backends: algorithm=weighted, canary=false, health=/health/5s, ...
[MAIN] Hot Reload:  ENABLED
[MAIN] REST API:    /api/metrics, /api/history, /api/health
[MAIN] ═══════════════════════════════════════════════════
```

---

## Imports Overview

| Import | What from it is used |
|---|---|
| `intelligent-lb/config` | `config.Load()`, `*config.Config` |
| `intelligent-lb/internal/dashboard` | `NewHub`, `Hub.StartBroadcast`, `Hub.SetProvider` |
| `intelligent-lb/internal/entrypoint` | `NewManager`, `New`, `ResolveMiddlewares` |
| `intelligent-lb/internal/hotreload` | `NewWatcher` |
| `intelligent-lb/internal/logging` | `InitFileLogger` |
| `intelligent-lb/internal/middleware` | `NewBuilder`, `Chain` |
| `intelligent-lb/internal/router` | `NewManager`, `AddRoute` |
| `intelligent-lb/internal/service` | `NewManager`, `Stop`, `ImportMetrics` |
| `intelligent-lb/internal/tlsutil` | `GenerateSelfSigned` |
