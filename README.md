# ⚡ Intelligent Stateless Load Balancer

**Adaptive Routing · Fault Tolerance · Circuit Breaker · Real-time Dashboard**

> IOE Pulchowk Campus · Minor Project 2026
> Binod Pandey · Gaurav Dahal · Manish Kr. Rajbanshi

---

## Table of Contents

- [Overview](#overview)
- [Objectives](#objectives)
- [System Architecture](#system-architecture)
- [Project Structure](#project-structure)
- [Core Components](#core-components)
  - [1. Proxy Handler](#1-proxy-handler)
  - [2. Routing Algorithms](#2-routing-algorithms)
  - [3. Priority Classifier](#3-priority-classifier)
  - [4. Circuit Breaker](#4-circuit-breaker)
  - [5. Health Monitor](#5-health-monitor)
  - [6. Metrics Collector](#6-metrics-collector)
  - [7. Real-time Dashboard](#7-real-time-dashboard)
- [Request Lifecycle](#request-lifecycle)
- [Entrypoints](#entrypoints)
- [Configuration](#configuration)
- [Quick Start](#quick-start)
  - [Local Development](#local-development)
  - [Docker Deployment](#docker-deployment)
- [Testing & Evaluation](#testing--evaluation)
  - [Load Test](#load-test)
  - [Failure & Recovery Test](#failure--recovery-test)
  - [Chaos Mode](#chaos-mode)
  - [Dynamic Load Generator](#dynamic-load-generator)
  - [Race Detection](#race-detection)
- [Key Design Decisions](#key-design-decisions)
- [API Reference](#api-reference)
- [Technology Stack](#technology-stack)
- [License](#license)

---

## Overview

A stateless HTTP reverse-proxy load balancer built in Go that makes **intelligent routing decisions** based on real-time server performance metrics. Unlike traditional load balancers that distribute traffic blindly (round-robin) or based on static weights, this system continuously monitors backend latency and active connections, then routes each request to the optimal server using a weighted scoring formula.

The system features a **three-state circuit breaker** (CLOSED → OPEN → HALF_OPEN) for fault isolation, a **self-healing health monitor** that automatically detects and recovers failed servers, **priority-based request classification** (HIGH/LOW) that shifts routing weights for critical endpoints, and a **real-time WebSocket dashboard** for live monitoring.

---

## Objectives

| #  | Objective | Implementation |
|----|-----------|----------------|
| O1 | Stateless architecture supporting horizontal scalability | No shared state between requests; all metrics are in-memory per instance |
| O2 | Intelligent routing using real-time latency and load metrics | Weighted Score algorithm using `latencyWeight/(1+avgLatency) + loadWeight/(1+connections)` |
| O3 | Priority-based routing for critical requests | `X-Priority` header + URL-based auto-classification (`/api/payment`, `/api/auth`, `/admin`) |
| O4 | Self-healing with automatic failure detection and recovery | Health monitor with configurable interval; automatic circuit breaker reset on recovery |
| O5 | Circuit breaker to isolate failing services | Per-server three-state breaker with configurable threshold and recovery timeout |
| O6 | Performance evaluation: response time, success rate, recovery speed | Built-in load test, failure test, chaos mode, and real-time dashboard |

---

## System Architecture

```
                        ┌──────────────────────────────────────────────────┐
                        │              LOAD BALANCER (:8080)               │
                        │  ┌──────────┼───────────┐    ┌────────────────────────┐  │
                        │  │ ┌──────────────────┐ │    │    Routing Engine      │  │
                        │  │ │   Rule Router    │ │───►│                        │  │
                        │  │ │ (Path, Header..) │ │    │  ┌──────────────────┐  │  │
                        │  │ └─────────┬────────┘ │    │  │  Weighted Score  │  │  │
                        │  │           │          │    │  │  Round Robin     │  │  │
                        │  │ ┌─────────▼────────┐ │    │  │  Least Conn      │  │  │
                        │  │ │   Priority       │ │    │  └──────────────────┘  │  │
                        │  │ │  Classifier      │ │    └───────────┬────────────┘  │
                        │  │ │ (HIGH / LOW)     │ │                │               │
                        │  │ └──────────────────┘ │                │               │
                        │  └──────────┬───────────┘    ┌──────────▼────────────┐  │
                        │             │                │    Reverse Proxy      │  │
                        │  ┌──────────▼───┐            │   (HTTP Forwarding)   │  │
                        │  │   Metrics    │◄───────────┤                       │  │
                        │  │  Collector   │            └──────────┬────────────┘  │
                        │  └──────┬───────┘                       │               │
                        │         │                    ┌──────────▼────────────┐  │
                        │  ┌──────▼───────┐            │   Circuit Breakers    │  │
                        │  │  Dashboard   │            │   (per-server)       │  │
                        │  │  (WebSocket) │            └──────────┬────────────┘  │
                        │  │  [:8081]     │                       │               │
                        │  └──────────────┘            ┌──────────▼────────────┐  │
                        │                              │   Health Monitor      │  │
                        │                              │   (periodic checks)   │  │
                        │                              └───────────────────────┘  │
                        └──────────────────────────────────────────────────────────┘
                                               │
                        ┌──────────────────────▼──────────────────────────┐
                        │              BACKEND SERVERS                     │
                        │                                                  │
                        │  ┌────────┐  ┌────────┐  ┌────────┐  ┌────────┐│
                        │  │ Alpha  │  │ Beta   │  │ Gamma  │  │ Delta  ││
                        │  │ :8001  │  │ :8002  │  │ :8003  │  │ :8004  ││
                        │  │ 15ms   │  │ 50ms   │  │ 100ms  │  │ 20ms   ││
                        │  └────────┘  └────────┘  └────────┘  └────────┘│
                        └──────────────────────────────────────────────────┘
```

---

## Project Structure

```
intelligent-lb/
├── cmd/
│   └── loadbalancer/
│       └── main.go              # Application entry point
├── internal/
│   ├── balancer/
│   │   ├── router.go            # Server selection with health + circuit filtering
│   │   ├── weighted.go          # Weighted Score algorithm (primary)
│   │   ├── roundrobin.go        # Round Robin algorithm (fallback)
│   │   └── leastconn.go         # Least Connections algorithm
│   ├── proxy/
│   │   └── proxy.go             # HTTP reverse proxy handler
│   ├── priority/
│   │   └── classifier.go        # Request priority classification
│   ├── health/
│   │   ├── monitor.go           # Periodic health check goroutine
│   │   └── breaker.go           # Three-state circuit breaker
│   ├── entrypoint/
│   │   ├── entrypoint.go        # Named entrypoint lifecycle management (Traefik-inspired)
│   │   └── entrypoint_test.go   # Entrypoint unit tests
│   ├── metrics/
│   │   ├── collector.go         # Thread-safe metrics storage
│   │   └── reporter.go          # Terminal metrics table printer
│   └── dashboard/
│       └── hub.go               # WebSocket hub for real-time dashboard
├── backend/
│   └── server.go                # Simulated backend server with /toggle chaos endpoint
├── client/
│   ├── loadtest.go              # Concurrent load testing tool
│   ├── failuretest.go           # Failure detection & recovery time measurement
│   ├── chaos_mode.go            # Chaos monkey + stress test (dynamic failures)
│   └── dynamic_load.go          # Sine-wave oscillating traffic generator
├── config/
│   ├── config.go                # Configuration loader with defaults
│   └── config.json              # Runtime configuration file
├── internal/
│   ├── router/
│   │   ├── router.go            # Rule-based router manager
│   │   ├── rule.go              # Traefik-inspired rule syntax parser
│   │   └── matcher.go           # Matcher functions (PathPrefix, Method, etc.)
│   ├── config.go                # Configuration loader with defaults
│   └── config.json              # Runtime configuration file
├── web/
│   └── dashboard.html           # Real-time monitoring dashboard (WebSocket)
├── docker-compose.yml           # Multi-container orchestration
├── Dockerfile.lb                # Load balancer container image
├── Dockerfile.backend           # Backend server container image
├── go.mod                       # Go module definition
└── go.sum                       # Dependency checksums
```

---

## Core Components

### 1. Proxy Handler
**File:** `internal/proxy/proxy.go`

The central HTTP handler that processes every incoming request through this pipeline:
1. **Classify** the request priority (HIGH/LOW)
2. **Select** the optimal backend using the routing engine
3. **Record** the start of the request (active connections++)
4. **Forward** the request to the chosen backend
5. **Measure** round-trip latency
6. **Update** metrics (success/failure, latency, circuit state)
7. **Respond** to the client with the backend's response + `X-Handled-By` header

**Key behaviors:**
- HTTP 5xx responses from backends are treated as **failures** (triggers circuit breaker)
- HTTP 4xx responses are treated as **successes** (client errors, not backend faults)
- Network errors immediately record failure and return 502 Bad Gateway

---

### 2. Routing Algorithms
**Files:** `internal/balancer/weighted.go`, `roundrobin.go`, `leastconn.go`

Three pluggable algorithms, all implementing the `Algorithm` interface:

#### Weighted Score (Default) — `weighted`
The intelligent algorithm that makes this load balancer "smart":

```
Score = latencyWeight / (1.0 + avgLatencyMs) + loadWeight / (1.0 + activeConnections)
```

**Priority-dependent weights:**

| Priority | Latency Weight | Load Weight | Behavior |
|----------|---------------|-------------|---------- |
| LOW      | 0.6           | 0.4         | Balanced — considers both latency and load |
| HIGH     | 0.8           | 0.2         | Latency-sensitive — strongly prefers fastest server |

The server with the **highest** score wins. Lower latency → higher score. Fewer connections → higher score.

**Example:** With servers Alpha (10ms, 5 conn) and Beta (50ms, 0 conn):
- LOW priority: Alpha=0.121, Beta=0.412 → **Beta wins** (no connections outweighs latency)
- HIGH priority: Alpha=0.106, Beta=0.216 → **Beta wins** but margin narrows (latency matters more)

#### Round Robin — `roundrobin`
Simple sequential distribution: S1→S2→S3→S1→... Uses an atomic counter for thread safety.

#### Least Connections — `leastconn`
Routes to the server with the fewest active connections. Good for requests with variable processing times.

---

### 3. Priority Classifier
**File:** `internal/priority/classifier.go`

Determines whether a request is HIGH or LOW priority using this precedence:

1. **Explicit header wins:** If `X-Priority: HIGH` or `X-Priority: LOW` is set, use that value
2. **URL-based auto-classification:** Requests to these paths are automatically HIGH priority:
   - `/api/payment/*` — Payment processing
   - `/api/auth/*` — Authentication
   - `/api/critical/*` — Critical operations
   - `/admin/*` — Admin panel
   - `/health-check` — Health endpoints
3. **Default:** Everything else is LOW priority

---

### 4. Circuit Breaker
**File:** `internal/health/breaker.go`

A per-server three-state circuit breaker that isolates failing backends:

```
     RecordFailure()              timeout elapsed              RecordSuccess()
      (count >= 3)                                              (probe OK)
  ┌──────────────────┐       ┌──────────────────┐       ┌──────────────────┐
  │     CLOSED       │──────►│      OPEN        │──────►│    HALF_OPEN     │
  │  (normal flow)   │       │  (blocked)       │       │  (one probe)     │
  └──────────────────┘       └──────────────────┘       └────────┬─────────┘
         ▲                          ▲                            │
         │                          │ RecordFailure()            │
         │                          │ (probe failed)             │
         │                          └────────────────────────────┘
         │               RecordSuccess()                         │
         └───────────────────────────────────────────────────────┘
```

**Critical design detail — `IsOpen()` vs `CanSend()` separation:**
- `IsOpen()` → **Pure read** (no state changes). Used in the routing loop to filter candidates.
- `CanSend()` → **May transition** OPEN→HALF_OPEN. Called only on the single chosen server after selection.

This prevents the bug where multiple servers get simultaneously moved to HALF_OPEN when iterating candidates.

**Configuration:**
| Parameter | Default | Description |
|-----------|---------|-------------|
| `breaker_threshold` | 3 | Consecutive failures before OPEN |
| `breaker_timeout_sec` | 15 | Seconds before OPEN → HALF_OPEN |

---

### 5. Health Monitor
**File:** `internal/health/monitor.go`

A background goroutine that periodically checks every backend server's `/health` endpoint:

- **Healthy:** HTTP 200 → `SetHealth(true)` + `RecordSuccess()` on circuit breaker
- **Unhealthy:** Non-200 or unreachable → `SetHealth(false)` + `RecordFailure()` on circuit breaker
- **Nil response guard:** Checks `resp != nil` before `resp.Body.Close()` to prevent panics when servers are completely unreachable
- **Concurrent checks:** Each server is checked in its own goroutine for parallel health probing

---

### 6. Metrics Collector
**File:** `internal/metrics/collector.go`

Thread-safe storage for all server performance data:

| Metric | Type | Description |
|--------|------|-------------|
| `total_requests` | int64 | Total requests served |
| `success_count` | int64 | Successful responses (status < 500) |
| `failure_count` | int64 | Failed responses (status >= 500 or network error) |
| `avg_latency_ms` | float64 | Rolling average over last 50 requests |
| `active_connections` | int64 | Currently in-flight requests |
| `high_priority_count` | int64 | HIGH priority requests handled |
| `low_priority_count` | int64 | LOW priority requests handled |
| `circuit_state` | string | CLOSED / OPEN / HALF_OPEN |
| `is_healthy` | bool | Health check status |
| `last_checked` | string | Timestamp of last health check |

**Concurrency:** All reads use `RLock()`, all writes use `Lock()` via `sync.RWMutex`. The `Snapshot()` method returns a deep copy for safe concurrent reads in routing and dashboard.

**Latency calculation:** Uses a rolling window of the last 50 latency samples. Each new sample triggers a full recalculation of the average.

---

### 7. Real-time Dashboard
**Files:** `internal/dashboard/hub.go`, `web/dashboard.html`

A WebSocket-powered monitoring dashboard that broadcasts metrics snapshots every second to all connected clients.

**Features:**
- Live server status cards (UP/DOWN with color indicators)
- Four KPI summary cards (Total Requests, Success Rate, Healthy Servers, Active Connections)
- Request Distribution doughnut chart
- Latency line chart with 30-point rolling history
- Priority Distribution section
- Connection status indicator (Connected/Offline)
- Real-time clock

---

### 8. Rule-Based Routing
**Files:** `internal/router/router.go`, `internal/router/rule.go`

A Traefik-inspired rule engine that evaluates incoming requests against configured matchers to dynamically determine the target backend service and middleware chain.

**Features:**
- Supports logical operators (`&&`, `||`, `()`) for complex matching logic.
- Built-in matchers: `PathPrefix('/api')`, `Path('/exact/path')`, `Method('POST')`, `Header('X-Internal', 'true')`, and `ClientIP('192.168.1.0/24')`.
- Routers evaluate in a deterministic priority order (highest priority first, breaking ties by preferring the longest rule string).
- Complete backward compatibility: if no routers match (or none are configured), the system seamlessly falls back to the legacy priority classifier and global server pool.

---

## Request Lifecycle

```
1. Client sends HTTP request to :8080
       │
2. entrypoint triggers top-level router manager
       │
3. Evaluate requests against configured rule routers
       ├── If match: apply router middlewares, select target service pool
       └── If no match: use legacy global server pool
       │
4. priority.Classify(path, header) → "HIGH" or "LOW"
       │
4. router.Select(priority)
       ├── metrics.Snapshot() → get fresh stats
       ├── Filter: IsHealthy && !IsOpen() → candidate list
       ├── algo.Select(candidates, stats, priority) → best server
       └── CanSend() on chosen server → may flip OPEN→HALF_OPEN
       │
5. metrics.RecordStart(target) → ActiveConnections++
       │
6. Forward request to backend (http.Client.Do)
       │
7. Measure latency = time.Since(start)
       │
8. On error:
       ├── metrics.RecordEnd(target, latency, false)
       ├── breaker.RecordFailure()
       ├── metrics.SetCircuitState()
       └── Return 502 Bad Gateway
       │
9. On success:
       ├── metrics.RecordEnd(target, latency, isSuccess)
       ├── breaker.RecordSuccess() or RecordFailure() (for 5xx)
       ├── metrics.SetCircuitState()
       ├── metrics.RecordPriority(target, priority)
       └── Copy response + X-Handled-By header to client
```

---

## Entrypoints

Inspired by [Traefik's EntryPoints](https://doc.traefik.io/traefik/routing/entrypoints/), each entrypoint runs as its own **independent HTTP server** with its own goroutine, middleware chain, and connection handling. Failure of one entrypoint does not affect others.

### Configuration

Add an `entrypoints` block to `config/config.json`:

```json
{
  "entrypoints": {
    "web": {
      "address": ":8080",
      "protocol": "http",
      "middlewares": ["headers", "cors", "rate-limit"]
    },
    "admin": {
      "address": ":8082",
      "protocol": "http",
      "middlewares": ["basic-auth"]
    },
    "dashboard": {
      "address": ":8081",
      "protocol": "http",
      "middlewares": ["basic-auth"]
    }
  }
}
```

### Entrypoint Fields

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `address` | Yes | — | Listen address (e.g. `:8080`, `0.0.0.0:9090`) |
| `protocol` | No | `http` | Protocol: `http` or `https` |
| `middlewares` | No | `[]` | List of middleware names applied to all traffic on this entrypoint |
| `tls` | No | — | Per-entrypoint TLS config (`cert_file`, `key_file`, etc.) |

### Available Middlewares

| Name | Description |
|------|-------------|
| `rate-limit` | Per-IP rate limiting (configured via `rate_limit_rps` and `rate_limit_burst`) |
| `headers` | Request header enrichment: `X-Forwarded-For`, `X-Real-IP`, `X-Request-ID` |
| `cors` | CORS headers + preflight `OPTIONS` handling |
| `basic-auth` | HTTP Basic Authentication (configured via `dashboard_auth`) |

### Backward Compatibility

If the `entrypoints` block is **not present**, entrypoints are automatically synthesized from the legacy `listen_port` and `dashboard_port` fields:

```
listen_port: 8080     →  entrypoint "web"       on :8080
dashboard_port: 8081  →  entrypoint "dashboard" on :8081
```

Old config files continue to work without any changes.

### Graceful Shutdown

On `SIGTERM` or `Ctrl+C`, all entrypoints stop accepting new connections, drain in-flight requests, then exit cleanly using `http.Server.Shutdown()` with a 30-second timeout.

### HTTPS per Entrypoint

```json
{
  "entrypoints": {
    "websecure": {
      "address": ":8443",
      "protocol": "https",
      "tls": {
        "cert_file": "server.crt",
        "key_file": "server.key"
      }
    }
  }
}
```

---

## Middleware Pipeline

Inspired by [Traefik's middleware architecture](https://doc.traefik.io/traefik/middlewares/overview/), the load balancer implements a **config-driven middleware pipeline**. Each middleware wraps the next `http.Handler` in the chain, and the final handler is the proxy forwarder.

```
Request → Headers → AccessLog → RateLimit → Timeout → Retry → CircuitBreaker → Proxy → Backend
```

### Middleware Configuration

Middlewares are defined in a `middlewares` block in `config.json`. Each middleware has a unique name, a `type`, and type-specific `config`:

```json
{
  "middlewares": {
    "rate-limit": {
      "type": "rateLimit",
      "config": {
        "requests_per_second": 100,
        "burst": 50
      }
    },
    "basic-auth": {
      "type": "basicAuth",
      "config": {
        "username": "admin",
        "password": "secret"
      }
    },
    "retry": {
      "type": "retry",
      "config": {
        "attempts": 3,
        "initial_interval_ms": 100
      }
    },
    "access-log": {
      "type": "accessLog",
      "config": {
        "file_path": "./logs/access.log"
      }
    }
  }
}
```

Middlewares can be attached to **entrypoints** (applied to all traffic) or **routers** (applied to matching requests):

```json
{
  "entrypoints": {
    "web": {
      "address": ":8080",
      "middlewares": ["headers", "access-log", "rate-limit", "timeout"]
    }
  },
  "routers": {
    "payment-router": {
      "rule": "PathPrefix('/api/payment')",
      "middlewares": ["rate-limit", "retry"],
      "service": "fast-backends"
    }
  }
}
```

### Available Middleware Types

| Type | Name | Description |
|------|------|-------------|
| `rateLimit` | Rate Limiter | Per-client-IP rate limiting using token bucket. Returns 429 with `Retry-After`. |
| `basicAuth` | Basic Auth | HTTP Basic Authentication. 401 for missing, 403 for wrong credentials. |
| `retry` | Retry | Exponential backoff retry (100→200→400ms). Only retries on 5xx, never 4xx. Adds `X-Attempts` header. |
| `accessLog` | Access Logger | Structured JSON access log to file. Fields: timestamp, request_id, client_ip, method, path, status_code, latency_ms, bytes_sent. |
| `headers` | Request Headers | Enriches with `X-Forwarded-For`, `X-Real-IP`, `X-Forwarded-Proto`, `X-Request-ID`. |
| `timeout` | Priority Timeout | Sets context deadline by priority: HIGH=5s, MEDIUM=10s, LOW=20s. Configurable. |
| `circuitBreaker` | Circuit Breaker | Standalone CB middleware. Opens after N failures, returns 503. CLOSED→OPEN→HALF_OPEN. |
| `cors` | CORS | Sets CORS headers and handles preflight OPTIONS requests. |

### Middleware Config Reference

#### `rateLimit`
| Field | Default | Description |
|-------|---------|-------------|
| `requests_per_second` | 100 | Max sustained requests per second per client IP |
| `burst` | 200 | Maximum burst size allowed |

#### `basicAuth`
| Field | Default | Description |
|-------|---------|-------------|
| `username` | — | Required username |
| `password` | — | Required password |

#### `retry`
| Field | Default | Description |
|-------|---------|-------------|
| `attempts` | 3 | Maximum total attempts (including first) |
| `initial_interval_ms` | 100 | Initial backoff interval (doubles each retry) |

#### `accessLog`
| Field | Default | Description |
|-------|---------|-------------|
| `file_path` | `access.log` | Path to the JSON access log file |

#### `timeout`
| Field | Default | Description |
|-------|---------|-------------|
| `high_sec` | 5 | Timeout for HIGH priority requests |
| `medium_sec` | 10 | Timeout for MEDIUM priority requests |
| `low_sec` | 20 | Timeout for LOW priority requests |

#### `circuitBreaker`
| Field | Default | Description |
|-------|---------|-------------|
| `threshold` | 3 | Consecutive failures before circuit opens |
| `recovery_timeout_sec` | 15 | Seconds before OPEN → HALF_OPEN probe |

### Backward Compatibility

If the `middlewares` block is **not present** in `config.json`, the system falls back to legacy name-based middleware resolution using global config fields (`rate_limit_rps`, `dashboard_auth`, etc.). Existing config files work without changes.

## Configuration

Edit `config/config.json`:

```json
{
  "entrypoints": {
    "web": { "address": ":8080", "protocol": "http", "middlewares": ["headers", "cors", "rate-limit"] },
    "dashboard": { "address": ":8081", "protocol": "http", "middlewares": ["basic-auth"] }
  },
  "routers": {
    "payment-router": {
      "rule": "PathPrefix('/api/payment') || PathPrefix('/api/checkout')",
      "priority": 100,
      "middlewares": ["rate-limit", "cors"],
      "service": "fast-backends"
    },
    "default-router": {
      "rule": "PathPrefix('/')",
      "priority": 1,
      "service": "all-backends"
    }
  },
  "services": {
    "fast-backends": {
      "servers": [
        { "url": "http://localhost:8001", "name": "Alpha-Fast", "weight": 5 }
      ]
    },
    "all-backends": {
      "servers": [
        { "url": "http://localhost:8002", "name": "Beta-All", "weight": 3 },
        { "url": "http://localhost:8003", "name": "Gamma-All", "weight": 1 }
      ]
    }
  },
  "algorithm": "weighted",
  "health_interval_sec": 5,
  "breaker_threshold": 3,
  "breaker_timeout_sec": 15,
  "metrics_interval_sec": 10
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `entrypoints` | auto | Named entrypoints (see [Entrypoints](#entrypoints)) |
| `routers` | — | Named routing rules matching requests to target services |
| `services` | — | Named backend server pools for explicit routing |
| `listen_port` | 8080 | Legacy: port for the reverse proxy (used if no `entrypoints`) |
| `dashboard_port` | 8081 | Legacy: port for the dashboard (used if no `entrypoints`) |
| `algorithm` | `weighted` | Routing algorithm: `weighted`, `roundrobin`, `leastconn`, or `canary` |
| `health_interval_sec` | 5 | Seconds between health checks |
| `breaker_threshold` | 3 | Consecutive failures before circuit opens |
| `breaker_timeout_sec` | 15 | Seconds before attempting recovery (OPEN→HALF_OPEN) |
| `metrics_interval_sec` | 10 | Seconds between terminal metrics table prints |
| `shutdown_timeout_sec` | 15 | Seconds to wait during graceful shutdown |

---

## Quick Start

### Prerequisites
- Go 1.21 or later
- Docker & Docker Compose (for containerized deployment)

### Local Development

```bash
# 1. Clone and navigate
cd intelligent-lb

# 2. Start all four backend servers
go run backend/server.go 8001 15 Alpha &
go run backend/server.go 8002 50 Beta  &
go run backend/server.go 8003 100 Gamma &
go run backend/server.go 8004 20 Delta  &

# 3. Start the load balancer
go run cmd/loadbalancer/main.go

# 4. Test with curl
curl http://localhost:8080/api/test
curl -H 'X-Priority: HIGH' http://localhost:8080/api/payment

# 5. Open the dashboard
open http://localhost:8081
```

### Docker Deployment

```bash
# Build and start all services
docker compose up --build

# Test
curl http://localhost:8080/api/test

# Dashboard
open http://localhost:8081

# Cleanup
docker compose down
```

---

## Testing & Evaluation

### Load Test
Sends a batch of concurrent requests and reports throughput, latency percentiles, and per-server distribution:

```bash
go run client/loadtest.go -requests=300 -concurrency=20 -high=0.2
```

**Flags:**
| Flag | Default | Description |
|------|---------|-------------|
| `-requests` | 300 | Total requests to send |
| `-concurrency` | 20 | Concurrent goroutines |
| `-high` | 0.2 | Fraction of HIGH priority requests |
| `-url` | `http://localhost:8080/api/test` | Target URL |

**Sample Output:**
```
╔══════════════════════════════════════════╗
║         LOAD TEST RESULTS                ║
╠══════════════════════════════════════════╣
║ Total Requests  :                    300 ║
║ Throughput      :              136.2 rps ║
║ Success Rate    :                100.0%  ║
║ Avg Latency     :                34.3 ms ║
║ P95 Latency     :               110.0 ms ║
╠══════════════════════════════════════════╣
║  Alpha              44 reqs   44.0%      ║
║  Delta              39 reqs   39.0%      ║
║  Beta               11 reqs   11.0%      ║
║  Gamma               6 reqs    6.0%      ║
╚══════════════════════════════════════════╝
```

---

### Failure & Recovery Test
Continuously sends requests while you manually kill and restart backends. Measures exact detection and recovery times:

```bash
go run client/failuretest.go

# In another terminal, kill a backend:
lsof -ti:8002 | xargs kill -9

# Watch the test detect the failure, then restart:
go run backend/server.go 8002 50 Beta

# Watch it detect recovery
```

---

### Chaos Mode
The ultimate dynamic test — combines high-concurrency stress with a **Chaos Monkey** that randomly breaks and recovers servers:

```bash
go run client/chaos_mode.go
```

**What it does:**
1. **50 concurrent workers** continuously send requests (maintains visible Active Connections)
2. **Chaos Monkey** toggles a random server's health every 12 seconds via the `/toggle` endpoint
3. Toggled servers return HTTP 500 on both `/health` and `/` endpoints

**What to watch on the dashboard:**
- Active Connections fluctuating (15–30 range)
- Failures count increasing when a server is toggled OFF
- Status badge switching between UP ↔ DOWN
- Circuit state cycling through CLOSED → OPEN → HALF_OPEN → CLOSED
- Traffic redistributing away from failing servers in real-time

---

### Dynamic Load Generator
Generates oscillating traffic (sine wave between 5–25 RPS) to show smooth chart movements:

```bash
go run client/dynamic_load.go
```

---

### Race Detection
Verify thread safety of the entire system under concurrent load:

```bash
go run -race cmd/loadbalancer/main.go
```

---

## Key Design Decisions

### 1. IsOpen() vs CanSend() Separation
`IsOpen()` is a **pure read** used for filtering candidates in a loop. `CanSend()` **may mutate state** (OPEN→HALF_OPEN) and is called only on the single chosen server. This prevents the subtle bug where iterating over all servers with `CanSend()` would transition multiple circuits to HALF_OPEN simultaneously.

### 2. Stateless Design
No database or persistent storage is used. All metrics live in-memory. If the load balancer restarts, counters reset to zero. This is intentional — a load balancer's routing decisions should be based on current conditions, not historical data.

### 3. 5xx as Backend Failure
HTTP 5xx responses are treated as failures and contribute to circuit breaker counts. HTTP 4xx responses are treated as successes because they indicate a client error, not a backend fault.

### 4. Rolling Latency Window
Average latency is calculated over the last 50 samples, not all-time. This ensures the routing algorithm adapts to **current** performance rather than being anchored to historical patterns.

### 5. Atomic Backend Counters
The simulated backend server uses `sync/atomic.Int64` for its request counter, not a plain `int` with a mutex, ensuring zero contention under concurrent load.

### 6. Chaos Toggle Endpoint
Backend servers expose a `/toggle` endpoint that flips `isFailing` (atomic bool). When failing, both `/health` and `/` return HTTP 500. This allows testing the full failure-detection → circuit-break → self-healing pipeline without killing processes.

---

## API Reference

### Load Balancer Proxy (`:8080`)

| Method | Path | Headers | Description |
|--------|------|---------|-------------|
| ANY | `/*` | `X-Priority: HIGH\|LOW` (optional) | Proxied to the best backend. Response includes `X-Handled-By` header. |

### Backend Servers (`:8001–8004`)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Returns JSON with server name, port, request count, delay |
| GET | `/health` | Returns JSON health status (HTTP 200 = healthy) |
| GET | `/toggle` | Flips failure simulation (for chaos testing) |

### Dashboard (`:8081`)

| Path | Protocol | Description |
|------|----------|-------------|
| `/` | HTTP | Serves the monitoring dashboard HTML |
| `/ws` | WebSocket | Real-time metrics stream (1-second intervals) |

---

## Technology Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.21 |
| WebSocket | gorilla/websocket v1.5.1 |
| HTTP Server | net/http (stdlib) |
| Concurrency | sync.RWMutex, sync/atomic |
| Dashboard | Vanilla HTML/CSS/JS + Chart.js |
| Containerization | Docker + Docker Compose |
| Fonts | Inter, JetBrains Mono (Google Fonts) |

---

## License

MIT
