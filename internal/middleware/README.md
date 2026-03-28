# Package `middleware`

**Import path:** `intelligent-lb/internal/middleware`

The `middleware` package implements the entire HTTP processing pipeline for the load balancer. Every request passes through a composable chain of middleware functions before reaching the proxy handler. The architecture mirrors Traefik's middleware system: each piece of functionality is an independent, pluggable `Middleware` function that wraps an `http.Handler`.

---

## File Structure

```
middleware/
├── middleware.go        — Middleware type, Chain, Builder, config resolution, ServiceRegistry
├── headers.go           — RequestHeaders: request ID + proxy headers injection
├── auth.go              — BasicAuth: HTTP Basic Authentication
├── ratelimit.go         — PerIPRateLimiter: per-IP token-bucket rate limiting
├── cors.go              — CORS: Cross-Origin Resource Sharing headers
├── accesslog.go         — NewAccessLog: JSON access log per request
├── timeout.go           — NewPriorityTimeout: priority-aware context deadlines
├── circuitbreaker.go    — NewCircuitBreaker: response-based circuit breaker integration
├── retry.go             — NewRetry: exponential-backoff retry with server exclusion
├── ratelimit_test.go    — Rate limiter tests
└── retry_test.go        — Retry middleware tests
```

---

## `middleware.go` — Core Infrastructure

### `Middleware` Type
```go
type Middleware func(http.Handler) http.Handler
```
A `Middleware` is simply a function that takes a handler and returns a new handler that adds some processing. This is Go's standard middleware pattern.

### `Chain(middlewares ...Middleware) Middleware`
Composes multiple middlewares into a single one. Iterates in **reverse** to build the chain so request flow is **left-to-right**:

```go
func Chain(middlewares ...Middleware) Middleware {
    return func(final http.Handler) http.Handler {
        for i := len(middlewares) - 1; i >= 0; i-- {
            final = middlewares[i](final)
        }
        return final
    }
}
```

```
Chain(A, B, C)(handler) → A(B(C(handler)))
Request flow: Client → A → B → C → handler → C → B → A → Client
```

### `ServiceRegistry` Interface
```go
type ServiceRegistry interface {
    RecordCircuitBreakerResult(url string, success bool)
}
```
An abstraction that the circuit breaker middleware uses to report results back to the service layer without importing it directly (avoids circular imports). Implemented by `service.Manager`.

### `Builder` — Config-Driven Middleware Factory
```go
type Builder struct {
    cfg      *config.Config
    registry ServiceRegistry
    cache    map[string]Middleware
    mu       sync.Mutex
}
```
Resolves named middleware from `config.json`. Results are cached so each named middleware is instantiated only once (safe because middlewares are stateless function closures, except for `PerIPRateLimiter` which holds state internally).

#### `Build(name string) (Middleware, error)`
Resolution order:
1. **Cache lookup** — return cached instance if already built.
2. **Named block** — check `cfg.Middlewares[name]` for a typed config entry.
3. **Legacy fallback** — check well-known names like `"rate-limit"`, `"headers"`, `"cors"`.

#### `BuildChain(names []string) ([]Middleware, error)`
Calls `Build` for each name and returns an ordered slice ready to pass to `Chain(...)`.

#### Config-Driven Middleware Types

| `type` in JSON | Middleware | Extra Config Fields |
|---|---|---|
| `rateLimit` | `PerIPRateLimiter` | `requests_per_second`, `burst` |
| `basicAuth` | `BasicAuth` | `username`, `password` |
| `retry` | `NewRetry` | `attempts`, `initial_interval_ms` |
| `accessLog` | `NewAccessLog` | `file_path` |
| `headers` | `RequestHeaders` | _(none)_ |
| `timeout` | `NewPriorityTimeout` | `high_sec`, `medium_sec`, `low_sec` |
| `circuitBreaker` | `NewCircuitBreaker` | `threshold`, `recovery_timeout_sec` |
| `cors` | `CORS` | `allowed_origins`, `allowed_methods`, `allowed_headers` |

**Config example:**
```json
"middlewares": {
  "api-rate-limit": {
    "type": "rateLimit",
    "config": { "requests_per_second": 100, "burst": 20 }
  },
  "retry-policy": {
    "type": "retry",
    "config": { "attempts": 3, "initial_interval_ms": 100 }
  }
}
```

---

## `headers.go` — Request Header Enrichment

### `RequestHeaders() Middleware`
Injects standard proxy observability headers into every incoming request **before** downstream processing:

| Header Set | Source | Purpose |
|---|---|---|
| `X-Real-IP` | `RemoteAddr` (port stripped) | True client IP for downstream services |
| `X-Forwarded-For` | Appended to existing chain | Proxy chain: `"original, proxy1, lb"` |
| `X-Forwarded-Proto` | `"https"` if `r.TLS != nil` else `"http"` | Protocol the client used |
| `X-Forwarded-Host` | `r.Host` (only if not already set) | Original request host |
| `X-Request-ID` | Generated UUID v4 (or passthrough if already present) | Unique request tracing ID |

The `X-Request-ID` is also stored in the **request context** via `requestIDKey{}` so downstream components (logging, retry middleware) can retrieve it without re-parsing the header:
```go
ctx := context.WithValue(r.Context(), requestIDKey{}, reqID)
```

### `generateRequestID() string`
Generates a UUID v4-format string using `crypto/rand`:
```go
b := make([]byte, 16)
rand.Read(b)
b[6] = (b[6] & 0x0f) | 0x40  // version 4
b[8] = (b[8] & 0x3f) | 0x80  // variant 10xx
return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", ...)
```
UUID v4 format: `xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`

### `RequestIDFromContext(ctx context.Context) string`
Helper for downstream code to retrieve the request ID without knowing the internal context key type:
```go
func RequestIDFromContext(ctx context.Context) string {
    if id, ok := ctx.Value(requestIDKey{}).(string); ok { return id }
    return ""
}
```

---

## `auth.go` — Basic Authentication

### `BasicAuth(username, password string) Middleware`
Enforces HTTP Basic Auth on the load balancer's dashboard endpoint.

**Behavior:**
- If `username == ""` or `password == ""`, the middleware is **disabled** (returns `next` unchanged). This enables backward compatibility — if no credentials are configured, the dashboard is unsecured.
- Parses credentials using Go's `r.BasicAuth()` (which decodes the `Authorization: Basic base64(user:pass)` header).
- Compares with **`crypto/subtle.ConstantTimeCompare`** to prevent timing attacks that could reveal password length via response time differences.
- Returns `401 Unauthorized` with `WWW-Authenticate: Basic realm="Load Balancer Dashboard"` for both missing and wrong credentials. This causes browsers to show the login dialog again on wrong credentials.

```go
subtle.ConstantTimeCompare([]byte(user), []byte(username)) != 1 ||
subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1
```

---

## `ratelimit.go` — Per-IP Rate Limiting

### `PerIPRateLimiter` struct
```go
type PerIPRateLimiter struct {
    mu       sync.RWMutex
    limiters map[string]*ipLimiter  // IP → {rate.Limiter, lastSeen}
    rps      rate.Limit
    burst    int
}

type ipLimiter struct {
    limiter  *rate.Limiter
    lastSeen time.Time
}
```

Each client IP gets its own `rate.Limiter` (token bucket from `golang.org/x/time/rate`). This means one abusive client cannot exhaust the budget for all clients.

### `NewPerIPRateLimiter(rps float64, burst int) *PerIPRateLimiter`
Creates the limiter and spawns a **background cleanup goroutine** that runs every 3 minutes and removes entries not seen in >5 minutes, preventing memory leaks in long-running deployments.

### IP Extraction Priority (`extractIP`)
```go
1. r.Header.Get("X-Real-IP")      // most reliable (set by trusted upstream proxy)
2. r.Header.Get("X-Forwarded-For") // first IP before first comma
3. net.SplitHostPort(r.RemoteAddr) // fallback: direct TCP connection
```

### `Middleware()` — The Actual Middleware
```go
func (rl *PerIPRateLimiter) Middleware() Middleware {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ip := extractIP(r)
            limiter := rl.getLimiter(ip)
            if !limiter.Allow() {
                w.Header().Set("Retry-After", "1")
                http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

`limiter.Allow()` consumes one token. If the bucket is empty, it returns `false` immediately (non-blocking). Returns `429 Too Many Requests` with `Retry-After: 1`.

### `getLimiter(ip string) *rate.Limiter`
Uses a write lock to create a new limiter for unseen IPs and updates `lastSeen` for known ones:
```go
func (rl *PerIPRateLimiter) getLimiter(ip string) *rate.Limiter {
    rl.mu.Lock(); defer rl.mu.Unlock()
    if entry, ok := rl.limiters[ip]; ok {
        entry.lastSeen = time.Now()
        return entry.limiter
    }
    limiter := rate.NewLimiter(rl.rps, rl.burst)
    rl.limiters[ip] = &ipLimiter{limiter: limiter, lastSeen: time.Now()}
    return limiter
}
```

---

## `cors.go` — CORS Middleware

### `CORSConfig` struct
```go
type CORSConfig struct {
    AllowedOrigins []string  // ["*"] or specific origins
    AllowedMethods []string  // ["GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"]
    AllowedHeaders []string  // ["Content-Type", "Authorization", "X-Priority", ...]
    MaxAge         string    // preflight cache duration in seconds
}
```

### `DefaultCORSConfig() CORSConfig`
Returns a **permissive development config** with `"*"` origin and all common methods/headers including `X-Priority` (the load balancer's custom priority header).

### `CORS(cfg CORSConfig) Middleware`
Pre-computes joined strings from slices at construction time (runs once, not per request):
```go
origins := strings.Join(cfg.AllowedOrigins, ", ")
methods := strings.Join(cfg.AllowedMethods, ", ")
headers := strings.Join(cfg.AllowedHeaders, ", ")
```

Sets headers on **every** response. For `OPTIONS` preflight requests, returns `204 No Content` immediately without calling the next handler:
```go
if r.Method == http.MethodOptions {
    w.WriteHeader(http.StatusNoContent)
    return
}
```

---

## `accesslog.go` — Access Log Middleware

### `accessLogEntry` struct
```go
type accessLogEntry struct {
    Timestamp   string  `json:"timestamp"`
    RequestID   string  `json:"request_id,omitempty"`
    ClientIP    string  `json:"client_ip,omitempty"`
    Method      string  `json:"method"`
    Path        string  `json:"path"`
    Priority    string  `json:"priority,omitempty"`
    BackendName string  `json:"backend_name,omitempty"`
    BackendURL  string  `json:"backend_url,omitempty"`
    StatusCode  int     `json:"status_code"`
    LatencyMs   float64 `json:"latency_ms"`
    Attempts    int     `json:"attempts,omitempty"`
    BytesSent   int     `json:"bytes_sent"`
}
```

### `statusRecorder` — Response Wrapper
Records the HTTP status code and byte count written by the downstream handler. Intercepts `WriteHeader` and `Write` calls:
```go
type statusRecorder struct {
    http.ResponseWriter
    statusCode int   // captured status
    bytes      int   // total bytes written
    written    bool  // prevents double-capturing
}
```

The `written` flag prevents `statusCode` from being overwritten if `WriteHeader` is called multiple times (which can happen with some error handlers).

### `NewAccessLog(filePath string) Middleware`
If `filePath != ""`, opens the file in **append mode** (`O_APPEND|O_CREATE|O_WRONLY`). If the file cannot be opened, it logs a warning but continues — the middleware still logs to the terminal.

On each request:
1. Wraps `w` in a `statusRecorder`.
2. Calls `next.ServeHTTP(recorder, r)` — runs the entire downstream pipeline.
3. Reads `recorder.statusCode`, `recorder.bytes`, and response headers set by the proxy (`X-Handled-By`, `X-Backend-URL`, `X-Attempts`).
4. Computes `LatencyMs = float64(latency.Microseconds()) / 1000.0` (sub-millisecond precision).
5. Marshals the entry to JSON and writes to file + terminal.

**Terminal output format:**
```
[ACCESS] 192.168.1.5 GET 200 /api/products 12.34ms 1024B
```

**File output format (JSON):**
```json
{"timestamp":"2024-01-15T10:30:00Z","request_id":"abc123","client_ip":"192.168.1.5","method":"GET","path":"/api/products","backend_name":"backend-1","backend_url":"http://backend-1:8081","status_code":200,"latency_ms":12.34,"bytes_sent":1024}
```

---

## `timeout.go` — Priority-Aware Timeout

### `NewPriorityTimeout(highSec, mediumSec, lowSec int) Middleware`
Sets a `context.WithTimeout` deadline on each request based on its classified priority. Uses the same `priority.Classify()` logic as the proxy to ensure consistent classification.

```go
pri := priority.Classify(r.URL.Path, r.Header.Get("X-Priority"))
var timeout time.Duration
switch pri {
case "HIGH":   timeout = highTimeout
case "MEDIUM": timeout = mediumTimeout
default:       timeout = lowTimeout  // LOW and any unrecognized value
}
ctx, cancel := context.WithTimeout(r.Context(), timeout)
defer cancel()
```

**Default values (if not configured):**
| Priority | Default Timeout |
|---|---|
| HIGH | 5 seconds |
| MEDIUM | 10 seconds |
| LOW | 20 seconds |

The priority is also stored in the context via `priorityCtxKey{}` so downstream handlers can read it without re-classifying:
```go
func PriorityFromContext(ctx context.Context) string
```

---

## `circuitbreaker.go` — Circuit Breaker Middleware

### `NewCircuitBreaker(registry ServiceRegistry) Middleware`
This middleware **does not implement the circuit breaker logic itself** — that lives in `health.Breaker`. Instead, it acts as the **observer** that reports outcomes back to the breaker after each request.

It wraps the `ResponseWriter` in a `cbStatusRecorder` to capture the response status code and reads the `X-Backend-URL` response header (set by the proxy handler):
```go
backendURL := recorder.Header().Get("X-Backend-URL")
if backendURL == "" { return }  // request never reached a backend

if cb.registry != nil {
    cb.registry.RecordCircuitBreakerResult(backendURL, recorder.statusCode < 500)
}
```

`status < 500` → success (2xx, 3xx, 4xx are all considered successful from the circuit breaker's perspective since they indicate the backend responded).

### `cbStatusRecorder`
```go
type cbStatusRecorder struct {
    http.ResponseWriter
    statusCode int
    written    bool
}
```
Captures the first `WriteHeader` call. The `written` flag prevents double-capture.

---

## `retry.go` — Exponential Backoff Retry

### Overview
The retry middleware wraps the inner pipeline (including the proxy handler) and re-invokes it on failure, up to `maxAttempts` times, with exponential backoff between attempts.

### `retryResponseWriter` — Buffered Response Writer
```go
type retryResponseWriter struct {
    http.ResponseWriter
    statusCode int      // captured status
    body       []byte   // buffered response body
    headers    http.Header // buffered response headers
    committed  bool     // true after flush() is called
}
```
Buffers the entire response until the middleware decides whether to retry or commit. This is critical: if the backend returns a 500 and we're going to retry, we must **not** send any response to the client yet.

- `Header()` returns `rw.headers` (the buffer), not `rw.ResponseWriter.Header()`.
- `WriteHeader(code)` stores the code but does not send it.
- `Write(b)` appends to `rw.body` but does not send it.
- `flush()` copies buffered headers, writes the status, and writes the body to the real `ResponseWriter`.

### `NewRetry(maxAttempts int, initialIntervalMs int) Middleware`

**Retry loop:**
```go
for attempt := 1; attempt <= rm.maxAttempts; attempt++ {
    // Backoff before retry (skipped on attempt 1)
    if attempt > 1 {
        backoff := rm.calculateBackoff(attempt - 1)
        select {
        case <-time.After(backoff):    // backoff complete
        case <-r.Context().Done():     // client disconnected, abort
            http.Error(w, "Client disconnected during retry backoff", http.StatusGatewayTimeout)
            return
        }
    }

    lastWriter = newRetryResponseWriter(w)
    ctx := context.WithValue(r.Context(), retryAttemptKey{}, attempt)
    next.ServeHTTP(lastWriter, r.WithContext(ctx))

    if !isRetryable(lastWriter.statusCode) || attempt == rm.maxAttempts {
        break  // success, 4xx, or last attempt
    }

    // Extract failed backend URL and add to exclusion list
    if backendURL := lastWriter.headers.Get("X-Backend-URL"); backendURL != "" {
        excluded := ExcludedFromContext(r.Context())
        excluded = append(excluded, backendURL)
        ctx = context.WithValue(r.Context(), excludedKey{}, excluded)
    }
}
// Flush the final response
lastWriter.flush()
```

**`isRetryable(statusCode int) bool`**: Only `statusCode >= 500` triggers a retry. 4xx errors (client errors) are never retried.

### `calculateBackoff(attempt int) time.Duration`
```go
base := float64(initialIntervalMs) * math.Pow(2, float64(attempt-1))
jitter := rand.Float64() * 0.25 * base
return time.Duration(base + jitter) * time.Millisecond
```

| Attempt | Base (100ms initial) | With 25% jitter |
|---|---|---|
| 1→2 | 100ms | 100–125ms |
| 2→3 | 200ms | 200–250ms |
| 3→4 | 400ms | 400–500ms |

### Context Keys
```go
type retryAttemptKey struct{}   // value: int (current attempt number, 1-indexed)
type excludedKey struct{}       // value: []string (URLs to exclude from selection)
```

Public helpers:
```go
func AttemptFromContext(ctx context.Context) int         // current attempt (1 = first try)
func ExcludedFromContext(ctx context.Context) []string   // URLs already failed this request
```

These are read by the proxy handler on each attempt to build the correct exclusion list for the balancer router.

---

## Context Key Summary

| Context Key | Set By | Read By | Value Type |
|---|---|---|---|
| `requestIDKey{}` | `RequestHeaders` | `AttemptFromContext`, logging, `accesslog` | `string` |
| `priorityCtxKey{}` | `NewPriorityTimeout` | `PriorityFromContext` | `string` |
| `retryAttemptKey{}` | `NewRetry` | `AttemptFromContext` in proxy | `int` |
| `excludedKey{}` | `NewRetry` (on retry) | `ExcludedFromContext` in proxy | `[]string` |

---

## Recommended Chain Order

```
RequestHeaders    ← inject IDs and proxy headers first
CORS              ← handle preflight before Auth
BasicAuth         ← authenticate before expensive work
AccessLog         ← must wrap all handlers to capture final response
PriorityTimeout   ← set deadline before retry loop starts
Retry             ← wraps proxy, re-invokes on 5xx
CircuitBreaker    ← must wrap proxy to observe backend URL + status
[proxy.Handler]   ← final handler: select backend, forward request
```

---

## Dependencies

| Package | Role |
|---|---|
| `golang.org/x/time/rate` | Token bucket rate limiter for `PerIPRateLimiter` |
| `crypto/subtle` | Constant-time comparison in `BasicAuth` |
| `crypto/rand` | Secure UUID generation in `RequestHeaders` |
| `intelligent-lb/internal/logging` | JSON log writing in `retry.go` |
| `intelligent-lb/internal/priority` | `Classify()` used in `timeout.go` |
| `intelligent-lb/config` | Config types for `Builder` |
| `context` | Storing/reading per-request state (request ID, priority, attempt, exclusions) |
| `sync` | Mutex in `PerIPRateLimiter` and `accessLogger` |
| `math` | `math.Pow` for backoff calculation |
| `math/rand` | Jitter in backoff |
