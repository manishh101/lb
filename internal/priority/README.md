# Package `priority`

**Import path:** `intelligent-lb/internal/priority`

The `priority` package classifies incoming HTTP requests into priority tiers. This classification drives two downstream behaviors: (1) which server the balancer algorithm prefers (WeightedScore uses different latency/load weights per priority), and (2) how much time the request is allowed before it times out (via the timeout middleware).

---

## File Structure

```
priority/
├── classifier.go      — Classify function, highPriorityPaths var
└── classifier_test.go — Table-driven tests for all classification paths
```

---

## `classifier.go`

### `highPriorityPaths` — Automatic HIGH Priority Paths

```go
var highPriorityPaths = []string{
    "/api/critical",
    "/api/payment",
    "/api/auth",
    "/admin",
    "/health-check",
}
```

These paths are **automatically elevated** to `"HIGH"` priority regardless of any request headers. The reasoning:

| Path | Business Rationale |
|---|---|
| `/api/critical` | Explicitly marked as business-critical endpoints |
| `/api/payment` | Financial operations: payment failures = revenue loss |
| `/api/auth` | Authentication: delays cascade to all users waiting to log in |
| `/admin` | Administrative operations: timeouts leave the system in bad state |
| `/health-check` | Health checks must respond quickly or they trigger false failures |

---

### `Classify(path, headerValue string) string`

```go
func Classify(path, headerValue string) string {
    // 1. Explicit override via X-Priority header
    if headerValue == "HIGH" || headerValue == "LOW" {
        return headerValue
    }

    // 2. Path-based automatic classification
    for _, prefix := range highPriorityPaths {
        if strings.HasPrefix(path, prefix) {
            return "HIGH"
        }
    }

    // 3. Default
    return "LOW"
}
```

**Decision tree:**
```
X-Priority: HIGH  →  "HIGH"
X-Priority: LOW   →  "LOW"
X-Priority: ""    →  path match?
    ├── starts with /api/critical → "HIGH"
    ├── starts with /api/payment  → "HIGH"
    ├── starts with /api/auth     → "HIGH"
    ├── starts with /admin        → "HIGH"
    ├── starts with /health-check → "HIGH"
    └── anything else             → "LOW"
```

**Important precedence rule:** The `X-Priority` header **always wins over path rules**. This means:
- A client can **downgrade** a normally HIGH-priority path: `X-Priority: LOW` on `/api/auth` → `"LOW"`.
- A client can **upgrade** a normally LOW-priority path: `X-Priority: HIGH` on `/api/products` → `"HIGH"`.

This gives client applications fine-grained control when the path-based defaults aren't appropriate (e.g., a background batch job that calls `/api/auth` for token refresh but doesn't need fast-path routing).

---

## Effect on the System

### WeightedScore Algorithm Weights

```go
// In balancer/weighted.go
latencyWeight, loadWeight := 0.6, 0.4   // LOW default
if priority == "HIGH" {
    latencyWeight, loadWeight = 0.8, 0.2 // HIGH: strongly favor low-latency server
}
```

| Priority | latency weight | load weight | Effect |
|---|---|---|---|
| HIGH | 0.8 | 0.2 | Routes to the fastest responding backend |
| LOW | 0.6 | 0.4 | More evenly distributed, accepting slightly higher latency |

### Timeout Middleware

```go
// In middleware/timeout.go
case "HIGH":   timeout = highTimeout   // default: 5s
case "MEDIUM": timeout = mediumTimeout // default: 10s
default:       timeout = lowTimeout    // default: 20s (LOW)
```

HIGH-priority requests **fail fast** (short timeout). This is intentional: if a payment request can't complete in 5 seconds, returning an error quickly is better than leaving the client waiting 20 seconds. LOW-priority background tasks get more time.

### Metrics Counters

The proxy calls `metrics.RecordPriority(url, priority)` after each request, incrementing `HighPriorityCount` or `LowPriorityCount`. These counters appear in the dashboard as a traffic split visualization:

```
backend-1: HIGH=342 (34%), LOW=658 (66%)
```

### Access Log Field

The `priority` field is included in every access log JSON line:
```json
{"method":"POST","path":"/api/payment","priority":"HIGH","status_code":200,...}
```

---

## Where `Classify` Is Called

| Caller | When | Used For |
|---|---|---|
| `middleware/timeout.go` | On each request, before timeout context is set | Setting the context deadline |
| `proxy/proxy.go` | On each request attempt | Passing to `router.Select(priority, excluded)` |

Both callers use the same input (`req.URL.Path` and `req.Header.Get("X-Priority")`), so they always produce the same classification for the same request.

---

## Testing

`classifier_test.go` uses table-driven tests:

```go
cases := []struct {
    path     string
    header   string
    expected string
}{
    {"/api/products", "",      "LOW"},
    {"/api/auth",     "",      "HIGH"},
    {"/api/auth",     "LOW",   "LOW"},  // header overrides path
    {"/admin/users",  "",      "HIGH"},
    {"/foo",          "HIGH",  "HIGH"}, // header overrides default
    {"/",             "",      "LOW"},
}
```

---

## Dependencies

| Package | Role |
|---|---|
| `strings` | `strings.HasPrefix(path, prefix)` for path matching |
