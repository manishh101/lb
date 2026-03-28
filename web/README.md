# `web/` — Real-Time Dashboard

This directory contains the front-end code for the Intelligent Load Balancer's real-time observability dashboard. 

---

## File Structure

```
web/
└── dashboard.html  — Single-page application containing all HTML, CSS, and Client-Side JS
```

---

## `dashboard.html` Overview

The dashboard is a single-file, zero-dependency HTML application. It connects to the load balancer's WebSocket endpoint (`/ws`) to stream live metrics, render interactive traffic charts, and display the real-time status of backend servers and circuit breakers.

### Key Technologies Used:
1. **HTML5:** Semantic layout with a CSS Grid structure.
2. **Vanilla CSS3:** Custom premium styling with CSS variables (glassmorphism, animations, dark mode theme).
3. **Vanilla JavaScript:** No external frameworks (e.g., React or Vue). 
4. **Chart.js:** Included via CDN for rendering the live latency and request volume graphs.
5. **WebSockets:** For 1-second interval live data updates without HTTP polling.

---

## Features

### 1. Global Metrics Summary
A top-level dashboard card displaying cumulative metrics across all services:
- **Total Requests:** Total HTTP requests handled natively by the load balancer.
- **Total Errors:** Count of requests that resulted in an HTTP 5xx error or backend failure.
- **Success Rate:** Computed percentage of successful requests.
- **Active Connections:** The number of currently in-flight requests being actively proxied.

### 2. Live Traffic & Latency Charts
Powered by Chart.js, the dashboard visualizes the last 120 seconds of traffic data:
- **Requests Per Second (RPS):** A bar chart showing the volume of requests terminating at the load balancer every second.
- **P95 vs Average Latency:** A line chart comparing the 95th percentile latency against the average latency. The P95 metric is crucial for identifying tail-latency spikes that average metrics hide.

### 3. Service-Level Breakdown
The dashboard dynamically renders a card for each configured service (e.g., `fast-backends`, `all-backends`, `admin-backends`).
Each service card displays:
- The active **load balancing algorithm** (e.g., Weighted Score, Canary, Round Robin).
- Overall **service RPS**.
- Circuit breaker state (Open/Closed/Half-Open).

### 4. Backend Health & Performance Matrix
Within each service card, a detailed table lists every configured backend server:
- **Status:** Integrated with the health monitor (Healthy/Failing/Recovering/Dead).
- **Requests (HIGH / LOW):** Traffic distribution broken down by priority tier.
- **RPS:** Current requests per second hitting this specific backend.
- **Avg Latency:** The rolling average response time.
- **Circuit Breaker Events:** A mini-timeline of recent state transitions and failure reasons (e.g., `Timeout` or `HTTP 502`).

---

## WebSocket Protocol

The client-side JavaScript establishes a WebSocket connection to `ws://{host}:{port}/ws`. 

It listens for two types of JSON messages sent by the `dashboard` package:

1. **`history` Event:** Sent immediately upon connection. Contains the last 120 seconds of metrics (the ring buffer) to instantly populate the charts without waiting 2 minutes.
2. **`tick` Event:** Sent every 1 second. Contains the latest `DashboardSnapshot` to update the DOM and append a new data point to the charts.

---

## UI/UX Design (CSS)

The dashboard is designed to look like a modern, premium observability tool:
- **Color Palette:** Deep navy background (`#0A0F1C`) with neon accents (Cyan for RPS, Purple/Pink for latency, Emerald for healthy status, Rose for errors).
- **Glassmorphism:** Cards use semi-transparent backgrounds with subtle borders and backdrop blur.
- **Animations:** Smooth CSS transitions for hover effects, status tag pulses (e.g., a breathing green dot for Healthy), and dynamic DOM updates.
- **Responsive Grid:** The layout adapts to screen width using `display: grid` and `repeat(auto-fit, minmax(...))`.

---

## Implementation Details

- **Efficient DOM Updates:** To prevent excessive CPU usage and layout thrashing, the JavaScript uses targeted DOM manipulation. It updates only the specific `innerText` of elements whose IDs map to server names, rather than re-rendering entire tables on every 1-second tick.
- **Chart.js Optimization:** The chart instances are created once. On every tick, the new data point is `push()`ed to the dataset array, the oldest point is `shift()`ed out, and `chart.update('none')` is called to disable expensive re-animation of the entire canvas.
- **Automatic Reconnection:** If the load balancer is restarted (or during hot reload), the WebSocket connection drops. The JavaScript includes an exponential backoff reconnection loop to automatically re-establish the stream without user intervention.
