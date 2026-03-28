# `client/` — Testing and Debugging Tools

This directory contains four standalone Go programs for testing the load balancer under different conditions. All files use `//go:build ignore` — they are **never included in the main build** and must be run explicitly with `go run`. They are development utilities, not production code.

---

## File Structure

```
client/
├── loadtest.go     — Structured load test with latency/success statistics
├── chaos_mode.go   — Combined stress test + chaos monkey (random backend failures)
├── dynamic_load.go — Sine-wave oscillating traffic generator
└── failuretest.go  — Failure detection and recovery time measurement
```

---

## `loadtest.go` — Structured Load Test

### Purpose
Sends a fixed number of requests with configurable concurrency and priority split, then prints a formatted results table showing throughput, latency percentiles, success rate, and per-server traffic distribution.

### Usage
```bash
go run client/loadtest.go \
  -requests 500 \
  -concurrency 25 \
  -high 0.3 \
  -url http://localhost:8082/api/test
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-requests` | `300` | Total number of requests to send |
| `-concurrency` | `20` | Max simultaneous goroutines (uses semaphore channel) |
| `-high` | `0.2` | Fraction of requests sent as `X-Priority: HIGH` (first N requests) |
| `-url` | `http://localhost:8080/api/test` | Load balancer URL to target |

### How Concurrency Is Controlled
Uses a **buffered channel as a semaphore**:
```go
sem := make(chan struct{}, *concurrency)
sem <- struct{}{}  // blocks if at capacity
go func() {
    defer func() { <-sem }()  // release slot when done
    // ... send request
}()
```
This caps the number of simultaneous in-flight requests to `*concurrency`.

### Priority Split
The first `highPct * total` requests are labeled HIGH; the rest are LOW:
```go
pri := "LOW"
if float64(i)/float64(*total) < *highPct {
    pri = "HIGH"
}
req.Header.Set("X-Priority", pri)
```

### Read from Response
Reads `X-Handled-By` header from the response to build the per-server distribution map.

### Output
```
╔══════════════════════════════════════════╗
║         LOAD TEST RESULTS                ║
╠══════════════════════════════════════════╣
║ Total Requests  :                    500 ║
║ Elapsed Time    :            2.341s      ║
║ Throughput      :               213.6 rps║
║ Success Rate    :                99.2%   ║
║ Avg Latency     :               23.4 ms  ║
║ P95 Latency     :               48.1 ms  ║
║ HIGH Pri OK     :                    150 ║
║ LOW  Pri OK     :                    346 ║
╠══════════════════════════════════════════╣
║  Alpha-Fast       250 reqs  50.4%       ║
║  Delta-All        246 reqs  49.6%       ║
╚══════════════════════════════════════════╝
```

---

## `chaos_mode.go` — Chaos Monkey + Stress Test

### Purpose
Combines two concurrent test behaviors:
1. **High-concurrency stress** — 50 goroutines continuously hammer the load balancer.
2. **Chaos monkey** — randomly toggles one of 4 backends to FAILING state every 12 seconds.

### Usage
```bash
go run client/chaos_mode.go
```

### `stressTest(concurrency int)`
Launches `concurrency` goroutines, each running an infinite loop:
```go
for {
    req, _ := http.NewRequest("GET", lbURL, nil)
    if rand.Intn(4) == 0 {
        req.Header.Set("X-Priority", "HIGH")  // 25% HIGH priority
    }
    resp, err := client.Do(req)
    // ... record failure
    time.Sleep(10ms + rand.Intn(40ms))  // 10–50ms between requests per goroutine
}
```
Effective RPS = `(concurrency) / avgDelay = 50 / 0.030s ≈ 1600 RPS`

Every 5 seconds, prints cumulative request/failure totals.

### `chaosMonkey(interval time.Duration)`
```go
idx := rand.Intn(len(serverURLs))  // pick random server (0-3)
http.Get(serverURLs[idx] + "/toggle")  // call /toggle to flip health state
```
After each toggle, prints the server's new state. Tests:
- Health monitor detection speed (how fast does it notice the failure?)
- Circuit breaker trips correctly on the right server
- Traffic re-routes to remaining healthy servers
- Recovery when the server is toggled back to healthy

### Backend Endpoints Used
```go
serverURLs = []string{
    "http://localhost:8001/toggle",  // Alpha
    "http://localhost:8002/toggle",  // Beta
    "http://localhost:8003/toggle",  // Gamma
    "http://localhost:8004/toggle",  // Delta
}
```

---

## `dynamic_load.go` — Sine-Wave Traffic Generator

### Purpose
Generates oscillating traffic that simulates realistic traffic patterns — ramps up and down like real production load, rather than flat constant load.

### Usage
```bash
go run client/dynamic_load.go
```

### Algorithm
Uses a **sine wave with a 30-second period** to oscillate between ~5 and ~25 RPS:
```go
elapsed := time.Since(start).Seconds()
sinVal := math.Sin(elapsed * (2 * math.Pi / 30))  // period = 30s
rps := 15 + int(sinVal * 10)    // oscillates between 5 and 25 RPS
```

Every second:
1. Computes the current target RPS from the sine function.
2. Launches `rps` goroutines, each sending one request.
3. Sleeps 1 second.

This tests whether the load balancer's metrics (RPS display in dashboard) accurately track variable load, and whether the algorithms prefer low-latency servers more during high-load periods.

### Why Sine Wave?
- Sine wave naturally produces smooth acceleration and deceleration.
- Real traffic often shows predictable daily patterns (peak at noon, low at night).
- Period of 30 seconds is short enough to see multiple cycles during a demo.

---

## `failuretest.go` — Failure Detection and Recovery Time Measurement

### Purpose
Continuously sends requests at 2/second and **measures exactly** how long it takes the load balancer to:
1. **Detect** a backend failure (first non-200 response after failure injection).
2. **Recover** (first 200 response after coming back healthy).

### Usage
```bash
go run client/failuretest.go
```
Then in another terminal: kill or `kill -STOP` a backend server, then restart it.

### State Machine
```go
// Failure detection:
if status != 200 && !failureStarted {
    failureStarted = true
    failureDetectedAt = time.Now()
    log.Printf("[FAILURE DETECTED] at %s", failureDetectedAt)
}

// Recovery measurement:
if status == 200 && failureStarted && lastStatus != 200 {
    recoveryTime := time.Since(failureDetectedAt)
    log.Printf("[RECOVERY COMPLETE] Recovery time: %s", recoveryTime)
    failureStarted = false
}
```

### Output Example
```
[REQUEST #   0] status=200  server=Alpha      latency=15ms
[REQUEST #   1] status=200  server=Gamma      latency=23ms
... (kill backend here)
[FAILURE DETECTED] Request #3 failed (status 0) at 10:30:05.123
[REQUEST #   3] status=0    server=            latency=3001ms
[REQUEST #   4] status=200  server=Delta      latency=18ms
[REQUEST #   4] status=200  ...
... (restart backend here)
[RECOVERY COMPLETE] Server: Alpha | Recovery time: 8.456s
```

The recovery time indicates how quickly the health monitor detected recovery + the circuit breaker reset. Typically `recoveryTimeout + healthCheckInterval + 1–2 seconds`.

---

## Running All Tools Together

For the most comprehensive demo of the load balancer:

```bash
# Terminal 1-4: Start backends
go run backend/server.go 8001 10 Alpha
go run backend/server.go 8002 50 Beta
go run backend/server.go 8003 100 Gamma
go run backend/server.go 8004 5 Delta

# Terminal 5: Start load balancer
go run ./cmd/loadbalancer/

# Terminal 6: Generate sine-wave load
go run client/dynamic_load.go

# Terminal 7: Run chaos monkey
go run client/chaos_mode.go

# Terminal 8: Measure recovery time
go run client/failuretest.go

# After testing: run structured load test
go run client/loadtest.go -requests 1000 -concurrency 50
```
