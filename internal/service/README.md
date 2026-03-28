# Package `service`

**Import path:** `intelligent-lb/internal/service`

The `service` package manages named backend service instances. Each service is a fully isolated runtime unit — with its own metrics, circuit breakers, health monitor, load-balancing router, and proxy handler. This design mirrors Traefik's `pkg/server/service/` package and enables multi-service configurations where different services can use different algorithms, backends, and circuit breaker settings.

---

## File Structure

```
service/
├── manager.go      — Manager, Instance, all lifecycle methods
└── manager_test.go — Tests for manager construction, metrics import, hot-reload
```

---

## `Instance` struct

```go
type Instance struct {
    Name      string
    Config    *config.ServiceConfig
    Collector *metrics.Collector
    Breakers  map[string]*health.Breaker
    Monitor   *health.Monitor
    Router    *balancer.Router
    Handler   http.Handler
}
```

Each `Instance` is a self-contained unit. Every field is independent — two services never share a `Collector` or `Breakers` map. This means:
- A backend URL can appear in multiple services (e.g., a shared auth server) and have separate per-service metrics.
- Circuit breakers for service A don't affect routing in service B.

---

## `Manager` struct

```go
type Manager struct {
    mu        sync.RWMutex
    instances map[string]*Instance  // service name → Instance
    cfg       *config.Config        // global config
}
```

The `Manager` provides:
- A central registry for all services.
- Aggregated metrics across all services for the dashboard.
- Implementation of `dashboard.SnapshotProvider` and `middleware.ServiceRegistry` interfaces.

---

## `NewManager(cfg *config.Config) *Manager`

Entry point. Creates the manager and immediately calls `buildAll(cfg)`:
```go
m := &Manager{instances: make(map[string]*Instance), cfg: cfg}
m.buildAll(cfg)
return m
```

---

## `buildAll(cfg *config.Config)`

Iterates `cfg.Services` (a `map[string]*ServiceConfig`) and calls `buildInstance` for each. Logs the created service:
```
[SERVICE] Created service "api-service": 3 servers, algorithm=weighted, canary=false
```

---

## `buildInstance(name string, svcCfg *config.ServiceConfig, cfg *config.Config) *Instance`

This is the most important function in the package. It assembles all 6 components for a single service:

### Step 1: Extract URLs, Names, Weights
```go
for _, srv := range svcCfg.Servers {
    urls   = append(urls,    srv.URL)
    names  = append(names,   srv.Name)
    weights = append(weights, srv.Weight)
}
```

### Step 2: Create Per-Service Metrics Collector
```go
collector := metrics.New(urls, names, weights)
algo := svcCfg.LoadBalancer.Algorithm
if svcCfg.Canary { algo = "canary" }  // Canary flag overrides algorithm field
collector.SetAlgorithm(algo)
```
The `Canary` field in config is a convenience toggle — it doesn't require changing `algorithm` explicitly.

### Step 3: Create Per-Service Circuit Breakers
```go
breakers := make(map[string]*health.Breaker)
for _, url := range urls {
    breakers[url] = health.NewBreaker(
        svcCfg.CircuitBreaker.Threshold,
        time.Duration(svcCfg.CircuitBreaker.RecoveryTimeoutSec)*time.Second,
    )
}
```
One `health.Breaker` per backend URL. Values come from the service's `circuit_breaker` config block.

### Step 4: Create and Start Health Monitor
```go
if len(svcCfg.Servers) > 0 {
    monitor = health.NewMonitor(svcCfg.Servers, collector, breakers)
    monitor.Start()  // immediately begins health-check goroutines
}
```
Only created if the service has servers (avoids nil goroutines for placeholder services).

### Step 5: Create Load Balancer Router
```go
algorithm := getAlgorithm(algo)  // string → Algorithm interface
router := balancer.NewRouter(urls, collector, breakers, algorithm)
```

### Step 6: Create Proxy Handler
```go
handler := proxy.New(router, collector, breakers, cfg.MaxRetries, cfg.PerAttemptTimeoutSec)
```
The proxy handler is the final `http.Handler` used by the HTTP router.

---

## Algorithm Resolution: `getAlgorithm(name string) balancer.Algorithm`

```go
switch name {
case "roundrobin": return &balancer.RoundRobin{}
case "leastconn":  return balancer.LeastConnections{}
case "canary":     return &balancer.Canary{}
default:           return balancer.WeightedScore{}  // handles "weighted" and any unknown value
}
```

`RoundRobin` and `Canary` use a pointer receiver because they hold state (`atomic.Uint64` and SWRR weights respectively). `WeightedScore` and `LeastConnections` are stateless value types.

---

## Manager Methods

### `Get(name string) http.Handler`
Returns the `http.Handler` for a named service. Used by the HTTP router to dispatch matched requests:
```go
route := routerManager.Route(req)
if route == nil { http.NotFound(w, r); return }
handler := svcManager.Get(route.Service)
handler.ServeHTTP(w, r)
```

### `GetInstance(name string) *Instance`
Returns the full `*Instance` including its collector, breakers, and monitor. Used during hot reloads to access the old instance for metrics import.

### `Instances() map[string]*Instance`
Returns a **copy** of the instances map (not the original) under a read lock. Prevents external code from mutating the map.

### `Stop()`
Stops health monitor goroutines for all service instances. Called during hot reload before the old manager is discarded:
```go
for name, inst := range m.instances {
    if inst.Monitor != nil {
        inst.Monitor.Stop()
        log.Printf("[SERVICE] Stopped health monitor for service %q", name)
    }
}
```

### `ImportMetrics(oldMgr *Manager)`
Preserves counters across hot reloads. For each service in the **new** manager, if the same service existed in the **old** manager, copies per-server stats:
```go
for name, inst := range m.instances {
    if oldSnap, ok := oldSnaps[name]; ok {
        inst.Collector.ImportMetrics(oldSnap)
    }
}
```
The `Collector.ImportMetrics()` method only copies data for URLs present in both old and new — new backends start at zero.

---

## Dashboard Integration

`Manager` implements `dashboard.SnapshotProvider`:

### `DashboardSnap() metrics.DashboardSnapshot`
Aggregates across all service instances:
```go
for _, inst := range m.instances {
    snap := inst.Collector.DashboardSnap()
    for url, stats := range snap.Servers {
        allServers[url] = stats      // merge all server stats maps
    }
    allEvents = append(allEvents, snap.CircuitEvents...)
    totalRPS += snap.GlobalRPS
}
```
Then computes global totals (`totalReq`, `totalOK`, `healthyCount`, `successRate`). Trims `allEvents` to the last 50.

### `GetConfig() *config.Config`
Returns the current global config (used by the dashboard's `/api/config` endpoint).

---

## Circuit Breaker Integration

`Manager` implements `middleware.ServiceRegistry`:

### `RecordCircuitBreakerResult(url string, success bool)`
Called by the circuit breaker middleware after each proxied request. Searches all instances for the backend URL:
```go
for _, inst := range m.instances {
    if b, ok := inst.Breakers[url]; ok {
        if success {
            if b.RecordSuccess() {               // returns true if state changed
                inst.Collector.ClearLatencies(url)  // clean up stale latencies
            }
        } else {
            b.RecordFailure()
        }
        inst.Collector.SetCircuitState(url, b.State())
        return
    }
}
```

---

## Hot Reload Pattern

```go
// 1. Build new manager from updated config
newMgr := service.NewManager(newCfg)

// 2. Preserve metrics counters from old manager
newMgr.ImportMetrics(oldMgr)

// 3. Stop old health check goroutines
oldMgr.Stop()

// 4. Update the dashboard to use the new provider
hub.SetProvider(newMgr)
```

Active connections to old backends are not interrupted — they're already in-flight through the old `proxy.Handler`. The new manager only handles new incoming connections.

---

## Dependencies

| Package | Role |
|---|---|
| `intelligent-lb/config` | `Config`, `ServiceConfig` |
| `intelligent-lb/internal/balancer` | `Router`, `WeightedScore`, `RoundRobin`, `LeastConnections`, `Canary` |
| `intelligent-lb/internal/health` | `Breaker`, `Monitor` |
| `intelligent-lb/internal/metrics` | `Collector`, `DashboardSnapshot`, `ServerStats` |
| `intelligent-lb/internal/proxy` | `Handler` — the actual HTTP proxy |
| `sync` | `RWMutex` for concurrent safe access |
| `time` | Circuit breaker recovery timeout |
| `log` | Service lifecycle logging |
| `net/http` | `http.Handler` interface |
