# Package `metrics`

**Import path:** `intelligent-lb/internal/metrics`

The `metrics` package is the central observability data store. It records real-time per-server performance data, computes statistics (average latency, P95, RPS), tracks circuit breaker transitions, and provides snapshots for both the load-balancing algorithms and the dashboard.

---

## File Structure

```
metrics/
├── collector.go      — Collector struct, all recording/query methods
├── collector_test.go — Tests for concurrent recording, P95, RPS
└── reporter.go       — Terminal ASCII table reporter
```

---

## `collector.go` — Core Data Store

### `ServerStats` struct
```go
type ServerStats struct {
    Name              string   `json:"name"`
    URL               string   `json:"url"`
    Weight            int      `json:"weight"`
    IsHealthy         bool     `json:"is_healthy"`
    AvgLatencyMs      float64  `json:"avg_latency_ms"`
    P95LatencyMs      float64  `json:"p95_latency_ms"`
    ActiveConnections int64    `json:"active_connections"`
    TotalRequests     int64    `json:"total_requests"`
    SuccessCount      int64    `json:"success_count"`
    FailureCount      int64    `json:"failure_count"`
    RetryCount        int64    `json:"retry_count"`
    CircuitState      string   `json:"circuit_state"`
    HighPriorityCount int64    `json:"high_priority_count"`
    LowPriorityCount  int64    `json:"low_priority_count"`
    LastChecked       string   `json:"last_checked"`
    recentLatencies   []float64  // unexported: rolling window, not serialized
}
```

`recentLatencies` is an **unexported** rolling window of the last 100 latency samples used for computing `AvgLatencyMs` and `P95LatencyMs`. It is not JSON-serialized; the computed statistics are.

### `Collector` struct
```go
type Collector struct {
    mu                sync.RWMutex
    servers           map[string]*ServerStats  // URL → stats pointer
    lastSnapshotTime  time.Time                // for RPS delta calculation
    lastTotalRequests int64                    // for RPS delta calculation
    circuitEvents     []CircuitEvent           // ring buffer (max 50)
    algorithm         string                   // for dashboard display
}
```

### `New(servers []string, names []string, weights []int) *Collector`
Initializes the collector with one `ServerStats` entry per URL. Aligns names and weights by index — if the slice is shorter, URL is used as the name and weight defaults to 1. Initially all servers are marked `IsHealthy: true` and `CircuitState: "CLOSED"`.

---

### Recording Methods (called on every request)

#### `RecordStart(url string)`
Called by the proxy **before** forwarding the request. Increments `ActiveConnections` under a write lock:
```go
s.ActiveConnections++
```

#### `RecordEnd(url string, latencyMs float64, success bool)`
Called by the proxy **after** the backend responds (or errors). This is the most complex recording method:

```go
s.ActiveConnections--
if s.ActiveConnections < 0 { s.ActiveConnections = 0 }  // guard against underflow
s.TotalRequests++

// Rolling window
s.recentLatencies = append(s.recentLatencies, latencyMs)
if len(s.recentLatencies) > 100 {
    s.recentLatencies = s.recentLatencies[1:]  // drop oldest
}

// Average latency
sum := 0.0
for _, v := range s.recentLatencies { sum += v }
s.AvgLatencyMs = sum / float64(len(s.recentLatencies))

// P95 latency
s.P95LatencyMs = computeP95(s.recentLatencies)

// Success / Failure counters
if success { s.SuccessCount++ } else { s.FailureCount++ }
```

**Rolling window rationale:** Using the last 100 samples smooths out single spikes while still reflecting recent performance. A server that was slow 1000 requests ago won't be penalized now.

#### `RecordRetry(url string)`
Increments `RetryCount` — called when the **retry middleware** decides to retry away from this server (meaning this server failed).

#### `RecordPriority(url, priority string)`
Splits traffic tracking by priority tier:
```go
if priority == "HIGH" { s.HighPriorityCount++ } else { s.LowPriorityCount++ }
```

---

### Health and Circuit Methods (called by health monitor)

#### `SetHealth(url string, healthy bool)`
Updates `IsHealthy` and sets `LastChecked` to the current time formatted as `"HH:MM:SS"`.

#### `SetCircuitState(url string, state string)`
Updates `CircuitState`. If the state **actually changed** (old ≠ new), appends a `CircuitEvent` to the ring buffer:
```go
event := CircuitEvent{
    ServerName: s.Name, ServerURL: url,
    OldState: oldState, NewState: state,
    Timestamp: time.Now().Format("15:04:05"),
}
c.circuitEvents = append(c.circuitEvents, event)
if len(c.circuitEvents) > 50 {
    c.circuitEvents = c.circuitEvents[len(c.circuitEvents)-50:]
}
```
Events are kept as a ring buffer capped at 50 entries.

#### `ClearLatencies(url string)`
Resets `recentLatencies`, `AvgLatencyMs`, and `P95LatencyMs` to zero. Called when a server **recovers from an outage** (circuit transitions from OPEN/HALF_OPEN to CLOSED). Pre-outage latency data would unfairly penalize the recovered server.

---

### Read Methods

#### `Snapshot() map[string]ServerStats`
Returns a **deep copy** of all server stats under a read lock. Used by the balancer algorithms — they need a consistent snapshot without holding the lock during their computation:
```go
snap := make(map[string]ServerStats, len(c.servers))
for url, s := range c.servers { snap[url] = *s }  // dereference pointer = copy
return snap
```

#### `ImportMetrics(oldSnap map[string]ServerStats)`
Restores counters from a previous collector (used during hot reload). Copies:
- `TotalRequests`, `SuccessCount`, `FailureCount`, `RetryCount`
- `HighPriorityCount`, `LowPriorityCount`
- `AvgLatencyMs`, `P95LatencyMs`

Only copies data for URLs present in **both** old and new collectors (new backends start at zero).

---

### `DashboardSnap() DashboardSnapshot`

Builds the aggregated snapshot sent to the dashboard. Uses a **write lock** (not read lock) because it also updates the `lastSnapshotTime` and `lastTotalRequests` for RPS calculation:

```go
// RPS computation
now := time.Now()
elapsed := now.Sub(c.lastSnapshotTime).Seconds()
delta := totalReq - c.lastTotalRequests
rps := float64(delta) / elapsed
c.lastSnapshotTime = now
c.lastTotalRequests = totalReq
```

**`DashboardSnapshot` struct:**
```go
type DashboardSnapshot struct {
    Servers       map[string]ServerStats `json:"servers"`
    GlobalRPS     float64                `json:"global_rps"`
    TotalRequests int64                  `json:"total_requests"`
    SuccessRate   float64                `json:"success_rate"`
    HealthyCount  int                    `json:"healthy_count"`
    TotalCount    int                    `json:"total_count"`
    Algorithm     string                 `json:"algorithm"`
    CircuitEvents []CircuitEvent         `json:"circuit_events"`
}
```

---

### `computeP95(latencies []float64) float64`
```go
func computeP95(latencies []float64) float64 {
    sorted := make([]float64, len(latencies))
    copy(sorted, latencies)     // don't mutate the original
    sort.Float64s(sorted)
    idx := int(math.Ceil(float64(n)*0.95)) - 1
    return sorted[idx]
}
```
P95 = "95% of requests completed in under X ms". Index calculation: `ceil(n × 0.95) - 1` (0-indexed). For n=20 samples: index = `ceil(19) - 1 = 18` (the 19th value in a sorted list of 20).

---

## `reporter.go` — Terminal Reporter

### `StartReporter(intervalSec int)`
Launches a goroutine that calls `PrintReport()` every `intervalSec` seconds:
```go
go func() {
    ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
    for range ticker.C { c.PrintReport() }
}()
```

### `PrintReport()`
Calls `DashboardSnap()` and prints a formatted table to stdout:
```
──────────────────────────────────────────────────────────────────────────────────────────────────────────────
  Server       Health   Requests  Avg(ms)  P95(ms)  Active  Retries  Success%  Circuit
──────────────────────────────────────────────────────────────────────────────────────────────────────────────
  backend-1    UP  ✓   1024      12.3     28.5     3       0        99.2      CLOSED
  backend-2    DOWN ✗  512       0.0      0.0      0       12       85.3      OPEN
──────────────────────────────────────────────────────────────────────────────────────────────────────────────
  TOTAL: 1536 requests  |  Success: 95.4%  |  RPS: 24.3  |  Healthy: 1/2  |  10:30:00
──────────────────────────────────────────────────────────────────────────────────────────────────────────────
```

The success rate per server is computed locally in `PrintReport()` as `(successCount/totalRequests)*100` (the global `SuccessRate` in `DashboardSnapshot` is for the system total, not per-server).

---

## Thread Safety Model

- All mutation methods (`RecordStart`, `RecordEnd`, `SetHealth`, etc.) use a **write lock**.
- `Snapshot()` uses a **read lock** — multiple goroutines can read simultaneously.
- `DashboardSnap()` uses a **write lock** because it updates the RPS baseline.
- `ImportMetrics()` uses a **write lock**.

The `recentLatencies` slice is always accessed under the write lock, so there is no concurrent-access issue despite being a slice (which is not goroutine-safe).

---

## Dependencies

| Package | Role |
|---|---|
| `sync` | `RWMutex` for thread safety |
| `math` | `math.Ceil` for P95 index |
| `sort` | Sorting latency window for P95 |
| `time` | RPS delta, `LastChecked`, circuit event timestamps |
| `fmt` / `strings` | Terminal table formatting in reporter |
