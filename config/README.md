# `config/` — Configuration Package

**Import path:** `intelligent-lb/config`

This directory contains the configuration type system, JSON parsing, defaults application, validation, and the canonical `config.json` used in development. The `config` package is imported by every other package in the project — it is the root of the dependency tree.

---

## File Structure

```
config/
├── config.go           — All config types, Load(), setDefaults(), validate()
├── config.json         — Main development config (used by cmd/loadbalancer/main.go)
├── config.docker.json  — Docker Compose variant (uses container DNS names)
└── config_test.go      — Tests for Load(), defaults, backward compatibility
```

---

## `config.go` — Type System

### Top-Level `Config` struct

The root configuration object parsed from `config.json`:

```go
type Config struct {
    // Legacy flat fields (backward compatible)
    ListenPort           int      `json:"listen_port"`
    DashboardPort        int      `json:"dashboard_port"`
    Servers              []ServerConfig
    Algorithm            string
    HealthInterval       int      `json:"health_interval_sec"`
    BreakerThreshold     int      `json:"breaker_threshold"`
    BreakerTimeoutSec    int      `json:"breaker_timeout_sec"`
    MetricsIntervalSec   int      `json:"metrics_interval_sec"`
    MaxRetries           int      `json:"max_retries"`
    ShutdownTimeoutSec   int      `json:"shutdown_timeout_sec"`
    RateLimitRPS         float64  `json:"rate_limit_rps"`
    RateLimitBurst       int      `json:"rate_limit_burst"`
    PerAttemptTimeoutSec int      `json:"per_attempt_timeout_sec"`
    RetryBackoffMs       int      `json:"retry_backoff_ms,omitempty"`
    RetryBackoffMaxMs    int      `json:"retry_backoff_max_ms,omitempty"`
    AccessLogPath        string   `json:"access_log_path,omitempty"`
    HotReload            bool     `json:"hot_reload,omitempty"`

    // Structured sub-configs
    DashboardAuth DashboardAuth            `json:"dashboard_auth,omitempty"`
    TLS           TLSConfig                `json:"tls,omitempty"`
    CORS          CORSConfig               `json:"cors,omitempty"`
    Timeouts      TimeoutConfig            `json:"timeouts,omitempty"`
    Middlewares   map[string]*MiddlewareConfig `json:"middlewares,omitempty"`
    EntryPoints   map[string]*EntryPointConfig `json:"entrypoints,omitempty"`
    Routers       map[string]*RouterConfig      `json:"routers,omitempty"`
    Services      map[string]*ServiceConfig     `json:"services,omitempty"`
}
```

---

### Config Sub-Types

#### `ServerConfig`
```go
type ServerConfig struct {
    URL         string            `json:"url"`
    Name        string            `json:"name"`
    Weight      int               `json:"weight"`       // 0 → defaulted to 1
    DelayMs     int               `json:"delay_ms"`     // for mock backends (unused by LB)
    HealthCheck HealthCheckConfig `json:"health_check,omitempty"`
}
```

#### `HealthCheckConfig`
```go
type HealthCheckConfig struct {
    Path           string `json:"path,omitempty"`            // default: "/health"
    IntervalSec    int    `json:"interval_sec,omitempty"`    // default: global health_interval_sec
    TimeoutSec     int    `json:"timeout_sec,omitempty"`     // default: 2
    ExpectedStatus int    `json:"expected_status,omitempty"` // default: 200
}
```

#### `CircuitBreakerConfig`
```go
type CircuitBreakerConfig struct {
    Threshold          int `json:"threshold"`            // failures before trip
    RecoveryTimeoutSec int `json:"recovery_timeout_sec"` // seconds before probe attempt
}
```

#### `LoadBalancerConfig`
```go
type LoadBalancerConfig struct {
    Algorithm string `json:"algorithm,omitempty"` // "weighted", "roundrobin", "leastconn", "canary"
    Sticky    bool   `json:"sticky,omitempty"`    // reserved for future session affinity
}
```

#### `ServiceConfig`
A named backend pool with its own independent reliability config:
```go
type ServiceConfig struct {
    LoadBalancer   *LoadBalancerConfig   `json:"load_balancer,omitempty"`
    HealthCheck    *HealthCheckConfig    `json:"health_check,omitempty"`
    CircuitBreaker *CircuitBreakerConfig `json:"circuit_breaker,omitempty"`
    Canary         bool                  `json:"canary,omitempty"`
    Servers        []ServerConfig        `json:"servers"`
}
```

#### `RouterConfig`
```go
type RouterConfig struct {
    Rule        string   `json:"rule"`                  // DSL expression e.g. "PathPrefix('/api')"
    Priority    int      `json:"priority"`              // higher = evaluated first
    Middlewares []string `json:"middlewares,omitempty"` // middleware names for this route
    Service     string   `json:"service"`               // target service name
}
```

#### `EntryPointConfig`
```go
type EntryPointConfig struct {
    Address     string     `json:"address"`              // e.g., ":8080"
    Protocol    string     `json:"protocol,omitempty"`   // "http" (default) or "https"
    Middlewares []string   `json:"middlewares,omitempty"` // entrypoint-level middleware names
    TLS         *TLSConfig `json:"tls,omitempty"`         // only set for https
}
```

#### `MiddlewareConfig`
Uses `json.RawMessage` for type-specific config (parsed lazily by the middleware builder):
```go
type MiddlewareConfig struct {
    Type   string          `json:"type"`             // e.g., "rateLimit", "retry"
    Config json.RawMessage `json:"config,omitempty"` // deferred JSON for type-specific fields
}
```

The `json.RawMessage` pattern allows the config package to stay decoupled from the middleware package — it doesn't need to know all middleware config structures.

#### `TLSConfig`
```go
type TLSConfig struct {
    Enabled      bool   `json:"enabled,omitempty"`
    CertFile     string `json:"cert_file,omitempty"`
    KeyFile      string `json:"key_file,omitempty"`
    AutoGenerate bool   `json:"auto_generate,omitempty"` // calls tlsutil.GenerateSelfSigned at startup
}
```

#### `TimeoutConfig`
```go
type TimeoutConfig struct {
    HighSec   int `json:"high_sec,omitempty"`    // default: 5
    MediumSec int `json:"medium_sec,omitempty"`  // default: 10
    LowSec    int `json:"low_sec,omitempty"`     // default: 20
}
```

---

## `Load(path string) (*Config, error)`

The entry point for all config consumers:

```go
func Load(path string) (*Config, error) {
    data, err := os.ReadFile(path)              // read raw JSON bytes
    json.Unmarshal(data, &cfg)                  // parse into Config struct
    setDefaults(&cfg)                           // fill in zeros
    if err := validate(&cfg); err != nil { ... }
    return &cfg, nil
```

---

## `setDefaults(cfg *Config)` — Default Values

Applied after parsing, before validation. Fills any zero-value fields:

| Field | Default |
|---|---|
| `ListenPort` | `8080` |
| `DashboardPort` | `8081` |
| `HealthInterval` | `5` (seconds) |
| `BreakerThreshold` | `3` |
| `BreakerTimeoutSec` | `15` |
| `MetricsIntervalSec` | `10` |
| `Algorithm` | `"weighted"` |
| `MaxRetries` | `3` |
| `ShutdownTimeoutSec` | `15` |
| `RateLimitRPS` | `100` |
| `RateLimitBurst` | `200` |
| `PerAttemptTimeoutSec` | `5` |
| `RetryBackoffMs` | `100` |
| `RetryBackoffMaxMs` | `5000` |
| `AccessLogPath` | `"access.log"` |
| `Timeouts.HighSec` | `5` |
| `Timeouts.MediumSec` | `10` |
| `Timeouts.LowSec` | `20` |
| Server `Weight` | `1` |
| Server `HealthCheck.Path` | `"/health"` |
| Server `HealthCheck.IntervalSec` | `cfg.HealthInterval` |
| Server `HealthCheck.TimeoutSec` | `2` |
| Server `HealthCheck.ExpectedStatus` | `200` |

### Backward Compatibility in `setDefaults`

**Legacy flat server list → service wrapping:**
```go
// If no "services" block but a flat "servers" list exists:
if len(cfg.Services) == 0 && len(cfg.Servers) > 0 {
    cfg.Services = map[string]*ServiceConfig{
        "default": { Servers: cfg.Servers },
    }
}
```
Old `config.json` files that use a flat `servers` list still work — they get wrapped in a `"default"` service automatically.

**Legacy ports → entrypoint synthesis:**
```go
if len(cfg.EntryPoints) == 0 {
    cfg.EntryPoints = map[string]*EntryPointConfig{
        "web":       {Address: fmt.Sprintf(":%d", cfg.ListenPort), Protocol: "http"},
        "dashboard": {Address: fmt.Sprintf(":%d", cfg.DashboardPort), Protocol: "http"},
    }
}
```
Old configs without an `entrypoints` block get two entrypoints synthesized from `listen_port` and `dashboard_port`.

### Access Log Directory Creation
```go
dir := filepath.Dir(cfg.AccessLogPath)
if dir != "." && dir != "" {
    os.MkdirAll(dir, 0755)  // create "logs/" directory if needed
}
```

---

## `validate(cfg *Config) error`

Currently performs one check: **no backend URL can appear in multiple services**.

```go
seenURLs := make(map[string]string)  // URL → first service name
for svcName, svc := range cfg.Services {
    for _, srv := range svc.Servers {
        if existingSvc, ok := seenURLs[srv.URL]; ok {
            return fmt.Errorf("backend URL %q is configured in multiple services (%q and %q)",
                srv.URL, existingSvc, svcName)
        }
        seenURLs[srv.URL] = svcName
    }
}
```

This prevents the circuit breaker `RecordCircuitBreakerResult` from ambiguously updating two different service instances for the same URL. If a backend should serve multiple routes, use separate service names with separate URLs (or reverse-proxy/Docker networking to the same process).

---

## `config.json` — Development Configuration

The primary configuration file used when running `go run ./cmd/loadbalancer/`:

```json
{
  "entrypoints": {
    "web":       { "address": ":8082", "protocol": "http" },
    "dashboard": { "address": ":8081", "protocol": "http" }
  },
  "middlewares": {
    "rate-limit":      { "type": "rateLimit",      "config": { "requests_per_second": 100, "burst": 50 } },
    "basic-auth":      { "type": "basicAuth",       "config": { "username": "admin", "password": "secret" } },
    "retry":           { "type": "retry",            "config": { "attempts": 3, "initial_interval_ms": 100 } },
    "access-log":      { "type": "accessLog",        "config": { "file_path": "./logs/access.log" } },
    "headers":         { "type": "headers",          "config": {} },
    "timeout":         { "type": "timeout",          "config": { "high_sec": 5, "medium_sec": 10, "low_sec": 20 } },
    "circuit-breaker": { "type": "circuitBreaker",   "config": { "threshold": 3, "recovery_timeout_sec": 15 } },
    "cors":            { "type": "cors",             "config": { "allowed_origins": ["*"] } }
  },
  "routers": {
    "payment-router": {
      "rule": "PathPrefix('/api/payment') || PathPrefix('/api/checkout')",
      "priority": 100, "service": "fast-backends"
    },
    "admin-router": {
      "rule": "PathPrefix('/admin') && Header('X-Admin-Key', 'secret')",
      "priority": 90, "service": "admin-backends"
    },
    "default-router": {
      "rule": "PathPrefix('/')", "priority": 1, "service": "all-backends"
    }
  },
  "services": {
    "fast-backends":  { "servers": [{ "url": "http://localhost:8001", "name": "Alpha-Fast", "weight": 5 }] },
    "admin-backends": { "servers": [{ "url": "http://localhost:8002", "name": "Beta-Admin", "weight": 3 }] },
    "all-backends":   { "servers": [
      { "url": "http://localhost:8003", "name": "Gamma-All", "weight": 1 },
      { "url": "http://localhost:8004", "name": "Delta-All", "weight": 4 }
    ]}
  }
}
```

## `config.docker.json` — Docker Compose Configuration

Identical structure to `config.json` but uses Docker Compose **service names** as hostnames instead of `localhost`:
```json
{ "url": "http://backend-alpha:8001", ... }
```
This works because Docker Compose creates an internal DNS for service-to-service communication.

---

## Config Reload (Hot Reload)

`config.Load()` is called again on every config file change (via `hotreload.NewWatcher`). Since `Load()` always creates a new `*Config` value (no global state), hot reload is inherently safe — the new config is built in isolation and then swapped in atomically.

---

## Dependencies

| Package | Role |
|---|---|
| `encoding/json` | `json.Unmarshal` and `json.RawMessage` |
| `os` | `os.ReadFile` for config file, `os.MkdirAll` for log directory |
| `path/filepath` | `filepath.Dir` to extract log directory path |
| `fmt` | Error message formatting |
