# ⚡ Intelligent Stateless Load Balancer

**Adaptive Routing & Fault Tolerance** — Go 1.21

> IOE Pulchowk Campus · Minor Project 2026
> Binod Pandey · Gaurav Dahal · Manish Kr. Rajbanshi

---

## Overview

A stateless HTTP load balancer with intelligent weighted routing, priority-based request classification, circuit breakers, self-healing health monitoring, and a real-time WebSocket dashboard.

## Objectives

| # | Objective |
|---|-----------|
| O1 | Stateless architecture supporting horizontal scalability |
| O2 | Intelligent routing using real-time latency and load metrics |
| O3 | Priority-based routing for critical requests |
| O4 | Self-healing mechanism with automatic failure detection and recovery |
| O5 | Circuit breaker to isolate failing services |
| O6 | Performance evaluation: response time, success rate, recovery speed |

## Architecture

```
Client → Load Balancer (Proxy) → Backend Servers (Alpha, Beta, Gamma, Delta)
              ├── Priority Classifier
              ├── Weighted Router (+ RoundRobin, LeastConn)
              ├── Circuit Breakers (per-server)
              ├── Health Monitor (5s interval)
              ├── Metrics Collector
              └── WebSocket Dashboard
```

## Quick Start (Local)

```bash
# 1. Start backends
go run backend/server.go 8001 15 Alpha &
go run backend/server.go 8002 50 Beta  &
go run backend/server.go 8003 100 Gamma &
go run backend/server.go 8004 20 Delta  &

# 2. Start load balancer
go run cmd/loadbalancer/main.go

# 3. Test
curl http://localhost:8080/api/test
curl -H 'X-Priority: HIGH' http://localhost:8080/api/payment

# 4. Dashboard
open http://localhost:8081
```

## Quick Start (Docker)

```bash
docker compose up --build

# Test
curl http://localhost:8080/api/test
open http://localhost:8081  # Dashboard
```

## Testing

```bash
# Load test
go run client/loadtest.go -requests=300 -concurrency=20

# Failure + recovery test
go run client/failuretest.go
# Then kill a backend: lsof -ti:8002 | xargs kill -9
# Then restart it:     go run backend/server.go 8002 50 Beta

# Race detector
go run -race cmd/loadbalancer/main.go
```

## Configuration

Edit `config/config.json`:

| Field | Default | Description |
|-------|---------|-------------|
| `listen_port` | 8080 | Proxy port |
| `dashboard_port` | 8081 | Dashboard port |
| `algorithm` | weighted | Routing algorithm (weighted/roundrobin/leastconn) |
| `health_interval_sec` | 5 | Health check interval |
| `breaker_threshold` | 3 | Failures before circuit opens |
| `breaker_timeout_sec` | 15 | Recovery timeout |

## Key Design Decisions

1. **IsOpen() vs CanSend()**: `IsOpen()` is a pure read for candidate filtering; `CanSend()` may transition OPEN→HALF_OPEN and is called only on the chosen server.
2. **Priority classification**: Explicit `X-Priority` header wins; URL-based auto-classification (`/api/payment`, `/admin/*`) is fallback.
3. **Atomic counters**: Backend request counter uses `sync/atomic.Int64` for data race safety.
4. **Nil response guard**: Health monitor checks `resp != nil` before `resp.Body.Close()`.

## License

MIT
