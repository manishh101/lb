# Package `proxy`

**Import path:** `intelligent-lb/internal/proxy`

The `proxy` package is the **final handler** in the load balancer's request pipeline. It receives requests that have already passed through all middleware (rate limiting, auth, logging, timeout, retry), selects a backend using the balancer router, forwards the request via an optimized HTTP transport, and records metrics.

---

## File Structure

```
proxy/
└── proxy.go  — Handler struct, New(), ServeHTTP(), proxyToBackend()
```

---

## Position in the Pipeline

```
Client → [rate-limit] → [auth] → [cors] → [access-log]
       → [timeout] → [retry] → proxy.Handler ← YOU ARE HERE
                                    ↓
                              balancer.Router.Select()
                                    ↓
                           backend HTTP server
```

The proxy is invoked **once per attempt**. The retry middleware above it re-invokes the proxy on 5xx failures, each time with an updated exclusion list in the request context.

---

## `Handler` struct

```go
type Handler struct {
    router            *balancer.Router
    metrics           *metrics.Collector
    breakers          map[string]*health.Breaker
    client            *http.Client
    maxRetries        int
    perAttemptTimeout time.Duration
}
```

| Field | Description |
|---|---|
| `router` | Selects a healthy backend URL for each request |
| `metrics` | Records `RecordStart`, `RecordEnd`, `RecordPriority` per request |
| `breakers` | Map of URL → circuit breaker (not directly used here; passed to router during construction) |
| `client` | Shared HTTP client with tuned transport for backend communication |
| `maxRetries` | Stored for reference; actual retry logic is in the middleware |
| `perAttemptTimeout` | Fallback timeout used if no context deadline is set |

---

## `New(...)` — Constructor and Transport Configuration

```go
func New(r *balancer.Router, m *metrics.Collector, b map[string]*health.Breaker,
    maxRetries int, perAttemptTimeoutSec int) *Handler
```

Creates the handler with a production-tuned `http.Transport`:

| Transport Setting | Value | Why |
|---|---|---|
| `MaxIdleConns` | 200 | Total idle connections across all backends |
| `MaxIdleConnsPerHost` | 50 | Idle connections reused per backend; reduces TCP handshake overhead |
| `MaxConnsPerHost` | 100 | Caps per-backend concurrency to prevent overload |
| `IdleConnTimeout` | 90s | Keep connections alive for 90s before closing (reduces reconnection cost) |
| `DisableCompression` | `true` | Don't auto-decompress backend responses — the body is streamed as-is to the client |
| `Dial.Timeout` | 5s | TCP connection establishment timeout |
| `Dial.KeepAlive` | 30s | TCP-level keep-alive probes interval |
| `ResponseHeaderTimeout` | 10s | Wait at most 10s for backend to start sending response headers |

The `http.Client` wraps this transport with no redirect following (`CheckRedirect` returns `http.ErrUseLastResponse` would be a further enhancement; currently default behavior applies).

---

## `ServeHTTP(w http.ResponseWriter, req *http.Request)`

The main handler method. Called by the retry middleware on each attempt.

### Step-by-step walkthrough:

**1. Extract context data** set by upstream middleware:
```go
requestID := middleware.RequestIDFromContext(req.Context())  // from headers.go
clientIP := req.Header.Get("X-Real-IP")                     // also set by headers.go
if clientIP == "" { clientIP = req.RemoteAddr }
```

**2. Classify priority** — re-classifies on every attempt. Uses `priority.Classify()`:
```go
pri := priority.Classify(req.URL.Path, req.Header.Get("X-Priority"))
```
This is consistent with the timeout middleware's classification.

**3. Read retry context** — which attempt number this is, and which backends to avoid:
```go
attempt := middleware.AttemptFromContext(req.Context())   // 1, 2, 3...
excluded := middleware.ExcludedFromContext(req.Context()) // URLs already failed
```

**4. Select a backend**:
```go
target, err := h.router.Select(pri, excluded)
```
If no healthy backend is available, responds with `502 Bad Gateway` and logs the error. Request processing stops here.

**5. Forward to backend** via `proxyToBackend(...)`.

---

## `proxyToBackend(w, req, target, pri, attempt, requestID, clientIP)`

**Timeout determination:**
```go
timeout := h.perAttemptTimeout  // configured fallback
if deadline, ok := req.Context().Deadline(); ok {
    remaining := time.Until(deadline)
    if remaining < timeout { timeout = remaining }  // use whichever is shorter
}
ctx, cancel := context.WithTimeout(req.Context(), timeout)
defer cancel()
```
This respects the priority-based deadline from `timeout.go` — if only 3s remain of a 5s HIGH-priority budget, the backend gets 3s, not 5s.

**Building the proxy request:**
```go
url := fmt.Sprintf("%s%s", target, req.RequestURI)  // full URL including path + query string
proxyReq, err := http.NewRequestWithContext(ctx, req.Method, url, req.Body)
```
`req.RequestURI` includes the path, query string, and fragment — everything after the host.

**Header forwarding:**
```go
for k, vals := range req.Header { proxyReq.Header[k] = vals }
```
All headers from the client request (including enriched ones from `RequestHeaders` middleware like `X-Real-IP`, `X-Forwarded-For`, `X-Request-ID`) are forwarded to the backend.

**Metrics recording:**
```go
h.metrics.RecordStart(target)
start := time.Now()
resp, err := h.client.Do(proxyReq)
latencyMs := float64(time.Since(start).Milliseconds())
// ...
h.metrics.RecordEnd(target, latencyMs, isSuccess)
h.metrics.RecordPriority(target, pri)
```

**Error handling:**
If the backend request fails (network error, timeout), responds `502 Bad Gateway` and logs the error. The retry middleware will then re-invoke `ServeHTTP` with the failed backend added to the exclusion list.

**Response writing:**
```go
serverName := h.metrics.GetName(target)
for k, vals := range resp.Header { w.Header()[k] = vals }  // forward all backend headers
w.Header().Set("X-Handled-By", serverName)      // display name of the backend
w.Header().Set("X-Backend-URL", target)         // backend URL (read by circuit breaker middleware)
w.Header().Set("X-Retry-Count", fmt.Sprintf("%d", attempt-1))
w.WriteHeader(resp.StatusCode)
io.Copy(w, resp.Body)                           // stream response body
```

**Why `X-Backend-URL` matters:** The circuit breaker middleware (which wraps the proxy) reads `X-Backend-URL` from the response after `ServeHTTP` returns to know which backend to record success/failure for.

---

## Success Determination
```go
isSuccess := resp.StatusCode < 500
```
HTTP 200–499 are all "successes" from the proxy's perspective (4xx are client errors, not backend errors). Only 5xx triggers a retry and a circuit breaker failure record.

---

## Dependencies

| Package | Role |
|---|---|
| `intelligent-lb/internal/balancer` | `Router.Select()` for backend selection |
| `intelligent-lb/internal/health`   | Circuit breaker map (passed through to router) |
| `intelligent-lb/internal/logging`  | `logging.Info()` / `logging.Error()` for structured request logs |
| `intelligent-lb/internal/metrics`  | `RecordStart`, `RecordEnd`, `RecordPriority`, `GetName` |
| `intelligent-lb/internal/middleware` | `RequestIDFromContext`, `AttemptFromContext`, `ExcludedFromContext` |
| `intelligent-lb/internal/priority` | `Classify()` for priority-based routing |
| `net/http` | HTTP client, handler, transport |
| `context` | Per-attempt timeout context |
| `io` | Body streaming via `io.Copy` |
| `net` | `net.Dialer` configuration for TCP keep-alives |
