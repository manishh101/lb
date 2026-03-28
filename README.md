# Intelligent Stateless Load Balancer

A production-ready, highly configurable, intelligent HTTP Load Balancer in Go, inspired by Traefik. It features a robust middleware pipeline, dynamic configuration hot-reloading, rule-based request routing, per-backend service management, canary traffic splitting, sophisticated observability metrics, and an advanced dark-themed real-time dashboard.

*(Minor Project Setup — IOE Pulchowk Campus, BCT 2026)*

##  Key Features

*   **Dynamic Rule-Based Routing:** Route requests based on `PathPrefix`, `Host`, and `Header` matchers across multiple prioritized routers.
*   **Per-Service Configuration & Isolation:** Define multiple backend services, each with entirely isolated health checks, circuit breakers, metrics (RPS, Latency), and load balancing behaviors.
*   **Load Balancing Algorithms:**
    *   **Weighted Round Robin:** Intelligently distribute traffic based on server weights.
    *   **Canary SWRR (Smooth Weighted Round Robin):** Smoothly distributes fractioned traffic to canary deployments based on precise weighting.
*   **Comprehensive Middleware Pipeline:**
    *   **Rate Limiting:** Token-bucket based RPS and burst control.
    *   **Retry with Exponential Backoff:** Transparently retries failed requests without hammering struggling servers.
    *   **Circuit Breaker:** State-machine based (CLOSED, OPEN, HALF-OPEN) preventing cascade failures with configurable thresholds.
    *   **Timeout & Priority Classes:** Differentiates timeouts between High/Medium/Low priority endpoints.
    *   **Basic Authentication:** Protect sensitive endpoints.
    *   **CORS & Headers:** Injects and cleans headers dynamically.
    *   **JSON Access Logging:** Formatted request tracking.
*   **Advanced Observability & Dashboard:**
    *   **WebSocket & REST APIs:** View live state via WebSocket, or pull metrics from `/api/metrics`, `/api/history`, and `/api/health` endpoints.
    *   **Premium Dark-Themed Dashboard:** A modern, glassmorphism UI with IBM Plex Mono aesthetics.
    *   **Analytics Engine:** High-performance ring buffer for **P95 Latency** and **RPS** tracking.
    *   **Historical Timeline:** View the last 120 seconds of traffic patterns instantly upon connection.
*   **Zero-Downtime Hot Reload:** Modify `config.json` on the fly — the load balancer natively computes diffs, dynamically adds/removes endpoints, scales weights, and swaps configurations without dropping active connections.
*   **Production Microservices Stack:** Fully containerized with Docker Compose, featuring built-in health checks and service dependencies.
*   **Kubernetes Ready:** Includes dedicated `/api/health` liveness probes.

##  Installation & Setup

You need Go (1.21+) installed.

```bash
# 1. Clone the repository
git clone https://github.com/manishh101/lb
cd intelligent-lb

# 2. Build the project
go build -o bin/loadbalancer ./cmd/loadbalancer

# 3. Run it locally
./bin/loadbalancer
```

##  Docker Compose Deployment (Recommended)

To run the load balancer along with 4 local backend instances (`alpha`, `beta`, `gamma`, `delta`) in a fully self-contained Docker bridge network:

```bash
# Provide a fully isolated microservices stack
docker compose up -d --build

# View real-time logs of the cluster
docker compose logs -f

# Shut down the stack
docker compose down
```

*Note: The Docker setup automatically maps the API gateway to `http://localhost:8082` and the Dashboard to `http://localhost:8081`.*

##  Configuration Reference (config.json)

The load balancer relies on a rich JSON configuration combining Entrypoints, Routers, Middlewares, and Services.

```json
{
  "entrypoints": {
    "web": {
      "address": ":8080",
      "protocol": "http",
      "middlewares": ["headers", "access-log", "rate-limit", "timeout"]
    },
    "dashboard": {
      "address": ":8081",
      "protocol": "http",
      "middlewares": ["basic-auth"]
    }
  },
  "routers": {
    "payment-router": {
      "rule": "PathPrefix('/api/payment')",
      "priority": 100,
      "middlewares": ["retry", "cors"],
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
      "canary": true,
      "health_check": {
        "path": "/health",
        "interval_sec": 5,
        "timeout_sec": 2,
        "expected_status": 200
      },
      "circuit_breaker": {
        "threshold": 3,
        "recovery_timeout_sec": 15
      },
      "servers": [
        {
          "url": "http://localhost:8001",
          "name": "Alpha-Fast",
          "weight": 90
        },
        {
          "url": "http://localhost:8002",
          "name": "Beta-Canary",
          "weight": 10
        }
      ]
    }
  },
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
    }
  },
  "hot_reload": true
}
```

##  Features Deep Dive

### Dynamic Canary Deployments
Setting `"canary": true` on a service immediately switches that specific service's algorithm to **Smooth Weighted Round Robin**. This guarantees precise interleaving. For example, a 90/10 weight configuration ensures exactly 1 in 10 requests goes to the canary server seamlessly, without clustering.

### Granular Metrics & Telemetry
- **P95 Latency:** Evaluated dynamically over a 100-sample sliding window to provide accurate tail latency.
- **Circuit Event Log:** Tracks and logs when an endpoint degrades (CLOSED -> OPEN) or attempts recovery (HALF-OPEN).
- **RPS Tracker:** Computes real requests per second based on snapshot deltas.
- **Retry Counters:** Visualizes exact backoff recovery events that rescued failed requests transparently.

### REST API
You don't need the UI to query metrics anymore.
- `GET /api/metrics` — Returns the current dashboard snapshot telemetry in JSON format.
- `GET /api/history` — Dumps the latest 120 historic snapshots for analytical chart plotting.
- `GET /api/health` — A basic 200 OK probe designed for container orchestrators (e.g. k8s liveness endpoints).

##  Performance Benchmarks (Verified)

Current architecture has been verified using a professional **Grafana k6** load test.

- **Peak Throughput:** ~233 Requests Per Second (RPS)
- **Latencies (P95):** 31.06ms
- **Success Rate:** 100% (Expected 2xx/429 statuses)
- **Concurrent Users:** 50 VUs

To reproduce these results, run the following docker command in your terminal:
```bash
docker run --rm -i --network intelligent-lb_lb-network grafana/k6 run - < load_test.js
```

## Viewing the Dashboard

1. Navigate to: `http://localhost:8081` (default auth: admin / secret)
2. You will see a dark IBM terminal-themed dashboard showing real-time RPS, active connections, total requests, P95 latency, and live circuit breaker transitions.

##  Project Contribution
Minor project for IOE Pulchowk Campus, BCT 2026.
Designed, documented, and built with modern High-Availability & SRE principles in mind.

## docker-use 

1. **Clone & Enter:**
   ```bash
   git clone <your-repo-ui> && cd intelligent-lb
   ```
2. **Launch Stack:**
   ```bash
   docker compose up -d
   ```
3. **Open Dashboard:** Visit `http://localhost:8081` (admin/secret)
4. **Professional Benchmarking:**
   If they want to run a professional stress test like you did:
   ```bash
   docker run --rm -i --network intelligent-lb_lb-network grafana/k6 run - < load_test.js
   ```


   ```bash
   docker run --rm -i --network intelligent-lb_lb-network grafana/k6 run - < traffic_generator.js
   ```