# `backend/` — Mock Backend Server

This directory contains a single self-contained Go program that acts as a **simulated backend HTTP server**. It is used for local development and testing — you run multiple instances on different ports (e.g., 8001, 8002, 8003) to create a pool of backends for the load balancer.

---

## File Structure

```
backend/
└── server.go  — Complete standalone HTTP server with chaos toggle
```

---

## Running the Backend

```bash
# Start 4 backends on ports 8001–8004:
go run backend/server.go 8001 10 Alpha   # port=8001, delay=10ms, name=Alpha
go run backend/server.go 8002 50 Beta    # port=8002, delay=50ms, name=Beta
go run backend/server.go 8003 100 Gamma  # port=8003, delay=100ms, name=Gamma
go run backend/server.go 8004 200 Delta  # port=8004, delay=200ms, name=Delta
```

### Command-Line Arguments

| Position | Variable | Default | Description |
|---|---|---|---|
| `os.Args[1]` | `port` | `8001` | Port to listen on |
| `os.Args[2]` | `delayMs` | `10` | Base response delay in milliseconds |
| `os.Args[3]` | `serverName` | `"Server"` | Display name (returned in responses) |

---

## Program-Level Variables

```go
var (
    port       = 8001
    delayMs    = 10
    serverName = "Server"
    reqCount   atomic.Int64  // thread-safe request counter
    isFailing  atomic.Bool   // chaos mode toggle; false = healthy
)
```

- `reqCount` uses `sync/atomic.Int64` — safe for concurrent increment from multiple goroutines without a mutex.
- `isFailing` uses `sync/atomic.Bool` — toggled by the `/toggle` endpoint; read by all request handlers.

---

## HTTP Endpoints

### `GET /` (and all other paths) — `apiHandler`

The primary endpoint. Simulates real backend work with a configurable delay:

```go
jitter := 0
if delayMs > 0 {
    jitter = rand.Intn(delayMs/2 + 1)  // adds 0 to 50% of base delay
}
time.Sleep(time.Duration(delayMs + jitter) * time.Millisecond)
```

The `jitter` makes response times realistic — backends rarely have perfectly consistent latency. A server with `delayMs=100` sleeps between 100ms and 150ms.

If `isFailing` is `true`, returns `500 Internal Server Error` immediately (after the delay — tests if the load balancer circuit breaker fires correctly even on slow failures).

**Response JSON:**
```json
{
  "handled_by":    "Alpha",
  "port":          8001,
  "request_count": 42,
  "delay_ms":      112,
  "path":          "/api/products"
}
```

**Response Header set:**
```
X-Server-Name: Alpha   ← for request tracing
```

### `GET /health` — `healthHandler`

Used by the load balancer's health monitor. Returns:
- `500 Internal Server Error` with body `"Simulated Chaos Failure"` if `isFailing == true`.
- `200 OK` with JSON body if healthy:
```json
{
  "status":          "UP",
  "port":            8001,
  "name":            "Alpha",
  "requests_served": 42
}
```

The load balancer's health check config (`"path": "/health"`) targets this endpoint.

### `GET /toggle` — `toggleHandler`

**Chaos mode control.** Flips `isFailing` between `true` and `false`. Used by the `client/chaos_mode.go` test tool to randomly kill backends.

```go
newState := !isFailing.Load()
isFailing.Store(newState)
```

Response: `200 OK` with body `"Server toggled to FAILING\n"` or `"Server toggled to HEALTHY\n"`.

This is the "chaos monkey" integration point — you can kill any backend by POSTing to its `/toggle` endpoint without actually stopping the process. This tests the load balancer's circuit breaker and health monitor without restarting anything.

---

## Docker Usage

In the Docker Compose setup (`docker-compose.yml`), multiple backend containers are launched using `Dockerfile.backend`:

```dockerfile
FROM golang:1.21-alpine
WORKDIR /app
COPY backend/server.go .
RUN go run server.go  # or compiled binary
```

Each container receives its port, delay, and name via environment variables or CMD arguments.

---

## Intentional Design Simplicity

The backend is intentionally minimal — it is purely a **test fixture**, not production code. It has:
- **No database** — just an in-memory counter.
- **No business logic** — just a sleep + JSON response.  
- **No authentication** — open to all requests.
- **No persistent state** — restart resets the counter.

This simplicity is ideal for testing the load balancer in isolation.
