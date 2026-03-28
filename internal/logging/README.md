# Package `logging`

**Import path:** `intelligent-lb/internal/logging`

The `logging` package provides structured JSON access logging for the load balancer. Every significant request event is serialized as a JSON line to both stdout and an optional file, enabling both real-time terminal monitoring and log aggregation by tools like Fluentd, Logstash, or `jq`.

---

## File Structure

```
logging/
└── logger.go  — AccessLog struct, Info(), Error(), InitFileLogger()
```

---

## Architecture

Two loggers coexist:
- **`defaultLogger`**: Always-on, writes JSON lines to `stdout` using Go's `log.Logger` with no prefix (timestamps are embedded in the JSON).
- **`fileLogger`**: Optional, writes to a file in append mode. Initialized once via `InitFileLogger()`. Protected by `fileMu sync.RWMutex` allowing concurrent reads (multiple goroutines can log simultaneously).

```go
var defaultLogger = log.New(os.Stdout, "", 0)  // no date/time prefix — JSON has timestamps

var (
    fileLogger *log.Logger
    fileMu     sync.RWMutex
)
```

---

## `AccessLog` struct

```go
type AccessLog struct {
    Time        string  `json:"time"`
    Level       string  `json:"level"`
    Message     string  `json:"msg"`
    RequestID   string  `json:"request_id,omitempty"`
    ClientIP    string  `json:"client_ip,omitempty"`
    Method      string  `json:"method,omitempty"`
    Path        string  `json:"path,omitempty"`
    Priority    string  `json:"priority,omitempty"`
    ServerName  string  `json:"server_name,omitempty"`
    Target      string  `json:"target,omitempty"`
    StatusCode  int     `json:"status_code,omitempty"`
    LatencyMs   float64 `json:"latency_ms,omitempty"`
    Attempt     int     `json:"attempt,omitempty"`
    BackoffMs   int64   `json:"backoff_ms,omitempty"`
    Error       string  `json:"error,omitempty"`
}
```

All fields except `Time`, `Level`, and `Message` use `omitempty` — they are omitted from the JSON if zero/empty. This keeps log lines concise.

| Field | When Present | Source |
|---|---|---|
| `time` | Always | Auto-set by `Info()`/`Error()` using `time.RFC3339Nano` |
| `level` | Always | `"INFO"` or `"ERROR"` |
| `msg` | Always | Caller-provided description |
| `request_id` | All proxied requests | From `middleware.RequestIDFromContext()` |
| `client_ip` | All proxied requests | From `X-Real-IP` or `RemoteAddr` |
| `method` | All proxied requests | HTTP method (GET, POST...) |
| `path` | All proxied requests | URL path |
| `priority` | All proxied requests | `"HIGH"` or `"LOW"` |
| `server_name` | Successful proxy | Backend display name |
| `target` | All proxied requests | Backend URL |
| `status_code` | Successful proxy | HTTP response code |
| `latency_ms` | All proxied requests | Backend response time |
| `attempt` | Retry logging | Which attempt number (1=first) |
| `backoff_ms` | Retry logging | Backoff duration before this attempt |
| `error` | Error events | Error message string |

---

## `InitFileLogger(path string) error`

Opens (or creates) the access log file in append mode:
```go
f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
// O_APPEND: all writes go to end of file (safe for multiple processes)
// O_CREATE: create file if it doesn't exist
// O_WRONLY: write-only
```

Creates a `log.Logger` writing to this file with no prefix. The file handle is never closed — the load balancer owns it for the process lifetime.

**Idempotent on the happy path:** If called twice with different paths, the second call wins (replaces `fileLogger`).

---

## `Info(entry AccessLog)`

```go
func Info(entry AccessLog) {
    entry.Time = time.Now().UTC().Format(time.RFC3339Nano)
    if entry.Level == "" { entry.Level = "INFO" }
    b, _ := json.Marshal(entry)
    defaultLogger.Println(string(b))  // stdout
    // file logger (if initialized):
    fileMu.RLock(); fl := fileLogger; fileMu.RUnlock()
    if fl != nil { fl.Println(string(b)) }
}
```

Automatically sets `Time` and defaults `Level` to `"INFO"`. Uses `json.Marshal` (not `json.NewEncoder`) to avoid trailing newline issues — `log.Logger.Println` adds its own newline.

The `fileMu.RLock()` pattern allows concurrent logging from multiple goroutines. The pointer itself is read atomically — Go guarantees pointer reads are atomic on 64-bit platforms, but the mutex is still used here for correctness on all platforms.

## `Error(entry AccessLog)`

Same as `Info` but always sets `Level = "ERROR"` (overrides any caller-set value).

---

## Sample Log Lines

**Successful request:**
```json
{"time":"2024-01-15T10:30:00.123456789Z","level":"INFO","msg":"Request proxied successfully","request_id":"a1b2-...","client_ip":"192.168.1.5","method":"GET","path":"/api/products","priority":"LOW","server_name":"backend-1","target":"http://backend-1:8081","status_code":200,"latency_ms":12.5,"attempt":1}
```

**Retry backoff:**
```json
{"time":"2024-01-15T10:30:01.123456789Z","level":"INFO","msg":"Retry middleware: backoff before retry","request_id":"a1b2-...","method":"GET","path":"/api/products","attempt":2,"backoff_ms":125}
```

**Error:**
```json
{"time":"2024-01-15T10:30:01.987654321Z","level":"ERROR","msg":"No healthy servers available","request_id":"a1b2-...","client_ip":"192.168.1.5","method":"GET","path":"/api/products","priority":"LOW","error":"no healthy servers available"}
```

---

## Usage Pattern

Callers populate only the relevant fields and leave others at their zero value (`omitempty` handles the rest):

```go
// In proxy handler — success:
logging.Info(logging.AccessLog{
    Message:    "Request proxied successfully",
    RequestID:  requestID,
    ClientIP:   clientIP,
    Method:     req.Method,
    Path:       req.URL.Path,
    Priority:   pri,
    ServerName: serverName,
    Target:     target,
    StatusCode: resp.StatusCode,
    LatencyMs:  latencyMs,
    Attempt:    attempt,
})

// In retry middleware — backoff:
logging.Info(logging.AccessLog{
    Message:   "Retry middleware: backoff before retry",
    RequestID: requestID,
    Method:    r.Method,
    Path:      r.URL.Path,
    Attempt:   attempt,
    BackoffMs: backoff.Milliseconds(),
})
```

---

## Thread Safety

- `defaultLogger` (stdout) — `log.Logger` is goroutine-safe internally.
- `fileLogger` — accessed via `fileMu.RLock()`/`RUnlock()`. Multiple goroutines can log to file simultaneously.
- `InitFileLogger` — not goroutine-safe; should only be called once at startup before any goroutines begin logging.

---

## Dependencies

| Package | Role |
|---|---|
| `encoding/json` | Marshal `AccessLog` struct to a JSON line |
| `log` | Thread-safe line writer to stdout and file |
| `os` | Open log file with `O_APPEND\|O_CREATE\|O_WRONLY` |
| `sync` | `RWMutex` for concurrent-safe file logger access |
| `time` | Timestamp generation in `RFC3339Nano` format |
