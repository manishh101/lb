# Package `balancer`

**Import path:** `intelligent-lb/internal/balancer`

The `balancer` package is the algorithmic core of the load balancer. It contains the `Algorithm` interface, four concrete implementations (WeightedScore, RoundRobin, LeastConnections, Canary), and the `Router` that connects algorithm selection to the health/circuit-breaker infrastructure.

---

## File Structure

```
balancer/
├── weighted.go      — Algorithm interface + WeightedScore implementation
├── roundrobin.go    — RoundRobin implementation
├── leastconn.go     — LeastConnections implementation
├── canary.go        — Canary (Smooth Weighted Round-Robin) implementation
├── router.go        — Router: candidate filtering + algorithm invocation
├── router_test.go   — Integration tests for router selection logic
├── canary_test.go   — Unit tests for SWRR distribution
└── weighted_test.go — Unit tests for WeightedScore scoring
```

---

## `weighted.go` — Algorithm Interface and WeightedScore

### `Algorithm` interface
```go
type Algorithm interface {
    Select(candidates []string, stats map[string]metrics.ServerStats, priority string) string
}
```
Every load-balancing strategy must implement this one method. It receives:
- `candidates` — a pre-filtered list of healthy, non-tripped server URLs (provided by `Router`)
- `stats` — a live snapshot of all server metrics from the `metrics.Collector`
- `priority` — `"HIGH"` or `"LOW"`, used by algorithms that want to behave differently per priority

Returns the URL of the chosen server, or `""` if no selection can be made.

---

### `WeightedScore` struct
```go
type WeightedScore struct{}
```
The default, production-grade algorithm. Scores each candidate using a formula that combines real-time **latency** and **active connections** with the server's **configured weight multiplier** and a small random **jitter** to break ties.

#### Scoring Formula
```
baseScore     = latencyWeight / (1 + avgLatencyMs)
              + loadWeight    / (1 + activeConnections)
score         = (baseScore × configuredWeight) + jitter
```

#### Priority Weights

| Priority | `latencyWeight` | `loadWeight` | Effect |
|---|---|---|---|
| `HIGH` | `0.8` | `0.2` | Strongly prefer the **lowest-latency** server |
| `LOW` | `0.6` | `0.4` | More even spread across servers |

**Why this works:**
- Division by `(1 + metric)` maps any positive metric to a score in `(0, 1]`. A server with `avgLatencyMs = 0` scores 1.0 on latency; one at 100ms scores ~0.0099.
- `configuredWeight` (from `config.json`) acts as a static multiplier. A server with `weight: 3` is three times more likely to win than one at `weight: 1` if all else is equal.
- `jitter` ∈ `[0, 0.001)` breaks exact ties at startup (when all latencies are 0), avoiding all traffic piling onto the same server.

#### Code walkthrough
```go
func (ws WeightedScore) Select(candidates []string, stats map[string]metrics.ServerStats, priority string) string {
    latencyWeight, loadWeight := 0.6, 0.4
    if priority == "HIGH" {
        latencyWeight, loadWeight = 0.8, 0.2
    }
    bestScore, bestServer := -1.0, candidates[0]
    for _, url := range candidates {
        s := stats[url]
        baseScore := latencyWeight/(1.0+s.AvgLatencyMs) + loadWeight/(1.0+float64(s.ActiveConnections))
        weightMultiplier := float64(s.Weight)
        if weightMultiplier <= 0 { weightMultiplier = 1.0 }
        jitter := rand.Float64() * 0.001
        score := (baseScore * weightMultiplier) + jitter
        if score > bestScore { bestScore = score; bestServer = url }
    }
    return bestServer
}
```

---

## `roundrobin.go` — RoundRobin

```go
type RoundRobin struct {
    counter atomic.Uint64
}
```

Distributes requests in a strict circular pattern using a **lock-free atomic counter**:
```go
idx := rr.counter.Add(1) - 1
return candidates[idx % uint64(len(candidates))]
```
- `Add(1)` returns the new value atomically. Subtracting 1 gives the 0-based index.
- Modulo wraps around the candidates slice.
- Completely ignores latency, load, and priority — this is intentional. RoundRobin is designed as a **startup fallback** before latency data accumulates.
- Thread-safe without any mutex due to `sync/atomic`.

---

## `leastconn.go` — LeastConnections

```go
type LeastConnections struct{}
```

Selects the candidate with the lowest `ActiveConnections`:
```go
best, bestConn := candidates[0], int64(1<<62)
for _, url := range candidates {
    if stats[url].ActiveConnections < bestConn {
        bestConn = stats[url].ActiveConnections
        best = url
    }
}
return best
```
- Initializes `bestConn` to a very large number (`1<<62`) so the first candidate always wins the first comparison.
- Best choice when backend request processing time is highly **variable** (e.g., mix of fast GETs and slow uploads).

---

## `canary.go` — Canary (Smooth Weighted Round-Robin)

```go
type Canary struct {
    mu             sync.Mutex
    serverWeights  map[string]int  // configured weights (immutable after init)
    currentWeights map[string]int  // SWRR running state (mutates every Select call)
    initialized    atomic.Bool
}
```

Implements the **NGINX Smooth Weighted Round-Robin (SWRR)** algorithm. Unlike `WeightedScore`, which uses dynamic performance metrics, Canary uses **static configured weights as fixed traffic percentages**.

A server with `weight: 90` receives exactly ~90% of traffic regardless of its current latency or connections. This is ideal for **canary deployments** where you want precisely 10% of traffic to go to a new version.

#### SWRR Algorithm (per `Select` call)
1. For each candidate, add its effective weight to `currentWeights[url]`.
2. Find the candidate with the highest `currentWeights` value — this is the winner.
3. Subtract the total weight from the winner's `currentWeights`.

#### Example: servers A(w=5), B(w=1), C(w=1); total=7
| Request | currentWeights before | Winner | currentWeights after |
|---|---|---|---|
| 1 | A=5, B=1, C=1 | A | A=-2, B=1, C=1 |
| 2 | A=3, B=2, C=2 | A | A=-4, B=2, C=2 |
| 3 | A=1, B=3, C=3 | B | A=1, B=-4, C=3 |
| 4 | A=6, B=-3, C=4 | A | A=-1, B=-3, C=4 |

Over 7 requests, A gets 5, B gets 1, C gets 1 — exactly per configured weights.

#### Initialization
`initWeights` is called lazily on the first `Select()`. It reads weights from the `metrics.ServerStats.Weight` field (populated from config) and initializes `currentWeights` to 0 for all servers. Uses `atomic.Bool` for a lock-free once-check with a mutex for the actual initialization.

#### New server handling
If a candidate appears in `Select()` but was not present during `initWeights()` (e.g., hot reload added it), it is assigned a default weight of 1 and added to both maps on the fly.

---

## `router.go` — Router

```go
type Router struct {
    servers  []string
    metrics  *metrics.Collector
    breakers map[string]*health.Breaker
    algo     Algorithm
}
```

The `Router` is the glue between the algorithm and the health/circuit-breaker system. Its `Select` method does all candidate filtering before invoking the algorithm.

### `Select(priority string, excluded []string) (string, error)`

**Step-by-step:**

1. **Get live stats**: calls `metrics.Snapshot()` — returns a deep copy of all `ServerStats` so the algorithm reads consistent data.

2. **Build exclusion map**: converts `excluded []string` into a `map[string]bool` for O(1) lookup. The `excluded` list is populated by the retry middleware when a server already failed this request.

3. **Candidate filtering** — a server is included in `candidates` only if ALL hold:
   - Not in `excluded` map
   - Present in `stats` map (i.e., known to the metrics system)
   - `stats[url].IsHealthy == true` (set by the health monitor)
   - `breakers[url].IsOpen() == false` (circuit is not tripped)

   > **Critical**: uses `IsOpen()` (pure read, no state change) — never `CanSend()` in the loop.

4. **Empty candidate check**: returns an error immediately if no candidates survive filtering.

5. **Algorithm selection**: passes `candidates`, `stats`, and `priority` to `algo.Select()`.

6. **Post-selection circuit check**: calls `breakers[chosen].CanSend()` exactly once on the chosen server. This may transition `OPEN → HALF_OPEN` to allow a probe request.

7. **Return**: returns the chosen URL, or an error if `CanSend()` returns false.

```go
func NewRouter(servers []string, m *metrics.Collector, b map[string]*health.Breaker, algo Algorithm) *Router
```

---

## Why `IsOpen()` vs `CanSend()` Matters

`CanSend()` has a **side effect**: it may transition the circuit from `OPEN` to `HALF_OPEN`. If you called it in the candidate-filtering loop, every unhealthy server would get probed simultaneously, defeating the purpose of controlled recovery. The Router ensures `CanSend()` is called **only on the single chosen server** — for at most one probe at a time.

---

## Dependencies

| Package | Role |
|---|---|
| `intelligent-lb/internal/metrics` | `Snapshot()`, `ServerStats.AvgLatencyMs`, `.ActiveConnections`, `.Weight`, `.IsHealthy` |
| `intelligent-lb/internal/health` | `Breaker.IsOpen()` (candidate filter), `Breaker.CanSend()` (post-selection) |
| `sync/atomic` | Lock-free counter in `RoundRobin` and `initialized` flag in `Canary` |
| `sync` | Mutex in `Canary` SWRR state updates |
| `math/rand` | Jitter in `WeightedScore` |
| `errors` | Error creation in `Router.Select` |
