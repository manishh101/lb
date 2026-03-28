# Package `health`

**Import path:** `intelligent-lb/internal/health`

The `health` package implements two reliability mechanisms: a **per-server circuit breaker** (`Breaker`) that automatically stops routing to failing backends, and a **health monitor** (`Monitor`) that periodically probes each backend via HTTP to detect outages and record resilience events.

---

## File Structure

```
health/
├── breaker.go      — Circuit breaker state machine
├── breaker_test.go — Tests covering all state transitions
└── monitor.go      — Periodic HTTP health-check goroutines
```

---

## `breaker.go` — Circuit Breaker

### Purpose
A circuit breaker protects the system from cascading failures by stopping requests to a struggling backend after it fails too many times. After a recovery timeout, one probe request is allowed through (`HALF_OPEN`). If the probe succeeds, normal traffic resumes.

### `State` type
```go
type State int

const (
    StateClosed   State = iota  // 0 — Normal: all requests flow through
    StateOpen                   // 1 — Tripped: all requests blocked
    StateHalfOpen               // 2 — Probe: one test request allowed
)

func (s State) String() string {
    return [...]string{"CLOSED", "OPEN", "HALF_OPEN"}[s]
}
```

### `Breaker` struct
```go
type Breaker struct {
    mu              sync.Mutex
    state           State
    failureCount    int
    threshold       int
    recoveryTimeout time.Duration
    lastFailureTime time.Time
}
```

| Field | Description |
|---|---|
| `mu` | Mutex protecting all state reads and writes |
| `state` | Current circuit state |
| `failureCount` | Number of consecutive failures since last reset |
| `threshold` | Number of failures that trips the circuit to `OPEN` |
| `recoveryTimeout` | Duration to stay in `OPEN` before allowing a probe |
| `lastFailureTime` | Timestamp of the last failure (used to calculate recovery window) |

### Constructor
```go
func NewBreaker(threshold int, recoveryTimeout time.Duration) *Breaker
```
Creates a new `Breaker` starting in `StateClosed`. Configured via `config.json`'s `circuit_breaker` block per service:
```json
"circuit_breaker": { "threshold": 5, "recovery_timeout_sec": 30 }
```

---

### Method: `IsOpen() bool`
```go
func (b *Breaker) IsOpen() bool {
    b.mu.Lock()
    defer b.mu.Unlock()
    if b.state != StateOpen {
        return false
    }
    return time.Since(b.lastFailureTime) <= b.recoveryTimeout
}
```
**Pure read** — no state changes. Returns `true` only if:
- The current state is `StateOpen`, AND
- The recovery timeout has **not yet elapsed**.

If the timeout has elapsed but the state is still `Open`, this returns `false` (the circuit is effectively expired, and `CanSend` will transition it on the next call). Used in `balancer.Router`'s candidate-filtering loop.

---

### Method: `CanSend() bool`
```go
func (b *Breaker) CanSend() bool {
    b.mu.Lock(); defer b.mu.Unlock()
    switch b.state {
    case StateClosed:   return true
    case StateOpen:
        if time.Since(b.lastFailureTime) > b.recoveryTimeout {
            b.state = StateHalfOpen  // ← STATE TRANSITION HERE
            return true              // allow one probe
        }
        return false
    case StateHalfOpen: return true
    }
    return false
}
```
**May cause a state transition**: `OPEN → HALF_OPEN` when the recovery timeout has elapsed. Called **only once per request**, on the single server chosen by the algorithm. Never called in a loop.

| State | Recovery elapsed? | Returns | Side Effect |
|---|---|---|---|
| CLOSED | N/A | `true` | None |
| OPEN | No | `false` | None |
| OPEN | Yes | `true` | Transitions to `HALF_OPEN` |
| HALF_OPEN | N/A | `true` | None (already probing) |

---

### Method: `RecordSuccess() bool`
```go
func (b *Breaker) RecordSuccess() bool {
    b.mu.Lock(); defer b.mu.Unlock()
    wasOpen := b.state != StateClosed
    b.failureCount = 0
    b.state = StateClosed
    return wasOpen
}
```
Resets the circuit to `CLOSED`. Clears `failureCount`. Returns `true` if the state was previously `OPEN` or `HALF_OPEN` (useful for triggering latency-clearing in the metrics collector — stale pre-outage latencies shouldn't skew routing).

---

### Method: `RecordFailure()`
```go
func (b *Breaker) RecordFailure() {
    b.mu.Lock(); defer b.mu.Unlock()
    b.failureCount++
    b.lastFailureTime = time.Now()
    if b.failureCount >= b.threshold || b.state == StateHalfOpen {
        b.state = StateOpen
    }
}
```
Two trip conditions:
1. `failureCount >= threshold` — accumulated failures exceed the limit.
2. `b.state == StateHalfOpen` — the recovery probe itself failed; immediately re-trip without waiting for threshold.

---

### State Transition Diagram

```
         RecordFailure() ≥ threshold
CLOSED ─────────────────────────────────► OPEN
  ▲                                        │
  │                                        │ recoveryTimeout elapsed
  │    RecordSuccess()               CanSend() transitions it
  │◄─────────────HALF_OPEN ◄───────────────┘
              RecordFailure()
              (probe failed)
                  │
                  ▼
                OPEN (re-trips immediately)
```

---

## `monitor.go` — Health Monitor

### Purpose
Sends periodic HTTP GET requests to each backend's health-check endpoint, updating the metrics and circuit breaker depending on the result.

### `Monitor` struct
```go
type Monitor struct {
    servers  []config.ServerConfig
    metrics  *metrics.Collector
    breakers map[string]*health.Breaker
    stopChs  []chan struct{}
    mu       sync.Mutex
}
```

| Field | Description |
|---|---|
| `servers` | List of backend server configurations |
| `metrics` | Shared metrics collector to update health status |
| `breakers` | Per-URL circuit breakers to record success/failure |
| `stopChs` | One stop channel per goroutine, used to shut them down |
| `mu` | Mutex protecting `stopChs` slice |

### Constructor
```go
func NewMonitor(servers []config.ServerConfig, m *metrics.Collector, b map[string]*health.Breaker) *Monitor
```

### `Start()`
```go
func (mon *Monitor) Start()
```
Launches **one goroutine per server**. Each goroutine:
1. Creates a `time.Ticker` with the server's `HealthCheck.IntervalSec`.
2. On each tick, calls `mon.check(server)`.
3. Exits when its stop channel is closed.

```go
go func() {
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:  mon.check(server)
        case <-stopCh:    return
        }
    }
}()
```

### `Stop()`
Closes all stop channels, gracefully terminating every health-check goroutine. Called during hot reloads to replace the monitor with a new one.

### `check(server config.ServerConfig)` — The Health Check Logic
```go
func (mon *Monitor) check(server config.ServerConfig)
```

Creates a fresh `http.Client` with `server.HealthCheck.TimeoutSec` timeout and sends a GET to `serverURL + healthCheckPath`.

**Failure path** (network error or wrong status code):
```go
mon.metrics.SetHealth(server.URL, false)
mon.breakers[server.URL].RecordFailure()
mon.metrics.SetCircuitState(server.URL, mon.breakers[server.URL].State())
log.Printf("[MONITOR] %-20s %-10s DOWN ✗ ...")
```

**Success path** (correct status code):
```go
mon.metrics.SetHealth(server.URL, true)
if mon.breakers[server.URL].RecordSuccess() {
    mon.metrics.ClearLatencies(server.URL)  // clear stale pre-outage latencies
}
mon.metrics.SetCircuitState(server.URL, mon.breakers[server.URL].State())
log.Printf("[MONITOR] %-20s %-10s UP   ✓")
```

The `ClearLatencies` call after recovery is important: a server that was down for 30 seconds may have very high latency readings from before the outage. Clearing them prevents the routing algorithm from penalizing the newly recovered server.

### Per-Server Health Check Configuration

Configured in `config.json` per server:
```json
{
  "name": "backend-1",
  "url": "http://backend-1:8081",
  "health_check": {
    "path": "/health",
    "interval_sec": 5,
    "timeout_sec": 2,
    "expected_status": 200
  }
}
```

| Config Field | Default | Description |
|---|---|---|
| `path` | `/health` | URL path to GET |
| `interval_sec` | 5 | Seconds between checks |
| `timeout_sec` | 2 | HTTP connect + response timeout |
| `expected_status` | 200 | Status code that means "healthy" |

---

## Key Design Decisions

1. **One goroutine per server**, not a single shared ticker — allows each server to have a completely independent interval and timeout.
2. **`RecordSuccess()` returns a bool** so the monitor knows when to clear latencies (only on recovery, not on every check).
3. **Circuit state is updated after both success and failure** — this keeps the dashboard's circuit state display always in sync with the breaker.

---

## Dependencies

| Package | Role |
|---|---|
| `intelligent-lb/config` | `ServerConfig` struct (URL, Name, HealthCheck sub-struct) |
| `intelligent-lb/internal/metrics` | `SetHealth`, `SetCircuitState`, `ClearLatencies` |
| `net/http` | HTTP client for probing |
| `sync` | Mutex for `stopChs` slice |
| `time` | Ticker interval and HTTP timeout |
