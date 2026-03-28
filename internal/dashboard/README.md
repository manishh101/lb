# Package `dashboard`

**Import path:** `intelligent-lb/internal/dashboard`

The `dashboard` package manages real-time observability for the load balancer. It handles WebSocket connections for live metric streaming, maintains a 2-minute sliding history buffer so new browser clients immediately see chart history, and exposes REST API endpoints for external tools (Grafana, monitoring scripts, Kubernetes probes).

---

## File Structure

```
dashboard/
└── hub.go  — Hub struct, NewHub, HandleWS, HandleAPIMetrics, HandleAPIHistory,
               HandleAPIConfig, HandleAPIHealth, StartBroadcast, SetProvider
```

---

## System Architecture

```
service.Manager (implements SnapshotProvider)
        ↓ DashboardSnap() every 1 second
dashboard.Hub
        ├── WebSocket clients (browser dashboard)
        │       → sends {"type":"history", "data":[...]} on connect
        │       → sends {"type":"tick", "data":{...}} every 1s
        ├── GET /api/metrics  → current snapshot as JSON
        ├── GET /api/history  → last 120 snapshots as JSON array
        ├── GET /api/config   → full current config as JSON
        └── GET /api/health   → 200/503 liveness probe
```

---

## `SnapshotProvider` Interface

```go
type SnapshotProvider interface {
    DashboardSnap() metrics.DashboardSnapshot
    GetConfig() *config.Config
}
```

The `Hub` does not depend on `service.Manager` directly — it uses this interface. This allows:
- **Single-service mode**: pass a `metrics.Collector` directly (if it implements the interface).
- **Multi-service mode**: pass a `service.Manager` which aggregates across all services.
- **Hot reload**: replace the provider atomically via `SetProvider()` without restarting the hub.

---

## `Hub` struct

```go
type Hub struct {
    provider SnapshotProvider
    clients  map[*websocket.Conn]bool  // set of active WebSocket connections
    mu       sync.Mutex                // protects clients and provider access
    htmlPath string                    // filesystem path to dashboard.html

    historyMu sync.RWMutex
    history   []metrics.DashboardSnapshot  // ring buffer of last 120 snapshots
}

const historySize = 120  // 2 minutes at 1-second intervals
```

### `NewHub(provider SnapshotProvider, htmlPath string) *Hub`
```go
return &Hub{
    provider: provider,
    clients:  make(map[*websocket.Conn]bool),
    htmlPath: htmlPath,
    history:  make([]metrics.DashboardSnapshot, 0, historySize),
}
```
`history` is pre-allocated with capacity `historySize` to avoid repeated memory reallocations.

### `SetProvider(p SnapshotProvider)`
Thread-safe provider swap used during hot reload:
```go
func (h *Hub) SetProvider(p SnapshotProvider) {
    h.mu.Lock(); defer h.mu.Unlock()
    h.provider = p
}
```

---

## WebSocket (`HandleWS`)

### Upgrade
```go
var upgrader = websocket.Upgrader{
    CheckOrigin: func(r *http.Request) bool { return true },
}
```
`CheckOrigin` always returns `true` — allows WebSocket connections from any origin. Appropriate for development; production might restrict this.

### Connection Handling
```go
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
    conn, err := upgrader.Upgrade(w, r, nil)
    // ...

    // 1. Send history buffer immediately (pre-populate charts)
    h.historyMu.RLock()
    if len(h.history) > 0 {
        historyMsg := map[string]interface{}{
            "type": "history",
            "data": h.history,
        }
        conn.WriteMessage(websocket.TextMessage, marshal(historyMsg))
    }
    h.historyMu.RUnlock()

    // 2. Register client
    h.mu.Lock()
    h.clients[conn] = true
    h.mu.Unlock()

    // 3. Keep-alive goroutine (blocks until disconnect)
    go func() {
        defer func() {
            h.mu.Lock(); delete(h.clients, conn); h.mu.Unlock()
            conn.Close()
        }()
        for {
            if _, _, err := conn.ReadMessage(); err != nil { break }
        }
    }()
}
```

**Why send history immediately on connect?** Without this, a new browser tab would show empty charts for up to 2 minutes while waiting for live ticks to accumulate. The history buffer delivers instant chart population.

**The keep-alive goroutine** reads (and discards) any messages from the client. When the browser closes the tab, `conn.ReadMessage()` returns an error (WebSocket close frame), which terminates the goroutine and removes the client from the map.

---

## REST API Handlers

### `ServeHTTP(w, r)` — Dashboard HTML
```go
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    http.ServeFile(w, r, h.htmlPath)
}
```
Serves the static `dashboard.html` file. The `Hub` itself implements `http.Handler` for the root path.

### `HandleAPIMetrics(w, r)` — `GET /api/metrics`
Returns the **current** `DashboardSnapshot` as JSON. Calls `h.provider.DashboardSnap()` under the hub mutex (to safely read `h.provider` in case of concurrent `SetProvider`):
```go
h.mu.Lock()
snap := h.provider.DashboardSnap()
h.mu.Unlock()
json.NewEncoder(w).Encode(snap)
```

### `HandleAPIHistory(w, r)` — `GET /api/history`
Returns the last 120 snapshots as a JSON array. Uses `historyMu.RLock()` (read lock) because history reads are read-only:
```go
h.historyMu.RLock(); defer h.historyMu.RUnlock()
json.NewEncoder(w).Encode(h.history)
```

### `HandleAPIConfig(w, r)` — `GET /api/config`
Returns the full current config. Useful for debugging: "what is the load balancer currently configured with?":
```go
h.mu.Lock(); cfg := h.provider.GetConfig(); h.mu.Unlock()
json.NewEncoder(w).Encode(cfg)
```

### `HandleAPIHealth(w, r)` — `GET /api/health`
**Kubernetes-compatible liveness/readiness probe.** Returns:
- **`200 OK`** with `{"status":"healthy","healthy_count":N,"total_count":M}` if at least one backend is healthy.
- **`503 Service Unavailable`** with `{"status":"unhealthy","healthy_count":0,"total_count":M}` if all backends are down.

This allows Kubernetes to stop routing traffic to the load balancer pod itself if all its backends have failed.

All API handlers set `Access-Control-Allow-Origin: *` to support monitoring tools and dashboards hosted on different origins.

---

## `StartBroadcast()` — Live Metric Streaming

```go
func (h *Hub) StartBroadcast() {
    go func() {
        ticker := time.NewTicker(1 * time.Second)
        for range ticker.C {
            // Get snapshot
            h.mu.Lock()
            snap := h.provider.DashboardSnap()
            h.mu.Unlock()

            // Append to history ring buffer
            h.historyMu.Lock()
            h.history = append(h.history, snap)
            if len(h.history) > historySize {
                h.history = h.history[len(h.history)-historySize:]  // keep last 120
            }
            h.historyMu.Unlock()

            // Broadcast to all connected clients
            tickMsg := map[string]interface{}{"type": "tick", "data": snap}
            data, _ := json.Marshal(tickMsg)
            h.mu.Lock()
            for conn := range h.clients {
                if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
                    conn.Close()
                    delete(h.clients, conn)  // remove disconnected client
                }
            }
            h.mu.Unlock()
        }
    }()
    log.Println("[DASHBOARD] WebSocket broadcast started (1s interval)")
}
```

**Ring buffer trimming:** `h.history[len(h.history)-historySize:]` slices from the tail, keeping only the most recent 120 entries. The slice is reassigned, so Go's garbage collector reclaims the older elements.

**Broken pipe handling:** `conn.WriteMessage()` returns an error if the client has disconnected. The loop immediately removes and closes the broken connection without crashing.

---

## WebSocket Message Protocol

| Message | Direction | When | Format |
|---|---|---|---|
| History | Server → Client | Immediately on connect | `{"type": "history", "data": [snapshot, ...]}` |
| Tick | Server → Client | Every 1 second | `{"type": "tick", "data": snapshot}` |

The `DashboardSnapshot` structure in both messages is identical, so the browser JavaScript can use the same rendering function for both.

---

## Locking Strategy

| Lock | Type | Protects |
|---|---|---|
| `mu` | `sync.Mutex` | `clients` map + `provider` pointer |
| `historyMu` | `sync.RWMutex` | `history` slice |

`historyMu` is an `RWMutex` because `HandleAPIHistory` and the on-connect history send both read it concurrently. `StartBroadcast` acquires a write lock to append. The `mu` is a plain `Mutex` because both reads and writes to `clients` are mutations (adding on connect, deleting on disconnect/error).

---

## Dependencies

| Package | Role |
|---|---|
| `github.com/gorilla/websocket` | WebSocket protocol: `Upgrader`, `Conn`, `WriteMessage`, `ReadMessage` |
| `intelligent-lb/internal/metrics` | `DashboardSnapshot` type |
| `intelligent-lb/config` | `*config.Config` type returned by `GetConfig()` |
| `encoding/json` | JSON serialization of snapshots and messages |
| `net/http` | `http.Handler`, `http.ServeFile`, `http.ResponseWriter` |
| `sync` | `Mutex` and `RWMutex` for concurrent-safe state |
| `time` | 1-second broadcast ticker |
| `log` | Startup and connection event logging |
