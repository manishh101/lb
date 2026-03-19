package dashboard

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"intelligent-lb/internal/metrics"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

const historySize = 120 // 2 minutes at 1-second intervals

// SnapshotProvider provides dashboard snapshots.
// This abstraction allows the hub to work with either a single collector
// or an aggregated view from the service manager.
type SnapshotProvider interface {
	DashboardSnap() metrics.DashboardSnapshot
}

// Hub manages WebSocket connections, broadcasts metrics, stores history,
// and serves REST API endpoints.
type Hub struct {
	provider SnapshotProvider
	clients  map[*websocket.Conn]bool
	mu       sync.Mutex
	htmlPath string

	// History buffer: ring buffer of last 120 snapshots for chart pre-population
	historyMu sync.RWMutex
	history   []metrics.DashboardSnapshot
}

// NewHub creates a new dashboard hub.
func NewHub(provider SnapshotProvider, htmlPath string) *Hub {
	return &Hub{
		provider: provider,
		clients:  make(map[*websocket.Conn]bool),
		htmlPath: htmlPath,
		history:  make([]metrics.DashboardSnapshot, 0, historySize),
	}
}

// SetProvider updates the snapshot provider (used during hot reload).
func (h *Hub) SetProvider(p SnapshotProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.provider = p
}

// ServeHTTP serves the dashboard HTML page.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, h.htmlPath)
}

// HandleWS upgrades HTTP connections to WebSocket and registers clients.
// On connect, the full history buffer is immediately sent to pre-populate charts.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[DASHBOARD] WebSocket upgrade failed: %v", err)
		return
	}

	// Send history buffer immediately on connect
	h.historyMu.RLock()
	if len(h.history) > 0 {
		historyMsg := map[string]interface{}{
			"type": "history",
			"data": h.history,
		}
		if data, err := json.Marshal(historyMsg); err == nil {
			conn.WriteMessage(websocket.TextMessage, data)
		}
	}
	h.historyMu.RUnlock()

	h.mu.Lock()
	h.clients[conn] = true
	h.mu.Unlock()
	log.Printf("[DASHBOARD] Client connected (%d total)", len(h.clients))

	// Keep connection alive; remove on disconnect
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.clients, conn)
			h.mu.Unlock()
			conn.Close()
			log.Printf("[DASHBOARD] Client disconnected (%d total)", len(h.clients))
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()
}

// HandleAPIMetrics serves GET /api/metrics — current full snapshot as JSON.
func (h *Hub) HandleAPIMetrics(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	snap := h.provider.DashboardSnap()
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(snap)
}

// HandleAPIHistory serves GET /api/history — last 120 snapshots as JSON array.
func (h *Hub) HandleAPIHistory(w http.ResponseWriter, r *http.Request) {
	h.historyMu.RLock()
	defer h.historyMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(h.history)
}

// HandleAPIHealth serves GET /api/health — 200 if at least one backend healthy,
// 503 if all backends are down. Kubernetes liveness probe compatible.
func (h *Hub) HandleAPIHealth(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	snap := h.provider.DashboardSnap()
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if snap.HealthyCount > 0 {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":        "healthy",
			"healthy_count": snap.HealthyCount,
			"total_count":   snap.TotalCount,
		})
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":        "unhealthy",
			"healthy_count": 0,
			"total_count":   snap.TotalCount,
		})
	}
}

// StartBroadcast sends metrics snapshots to all connected WebSocket clients
// at 1-second intervals. Each tick also stores the snapshot in the history buffer.
func (h *Hub) StartBroadcast() {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		for range ticker.C {
			h.mu.Lock()
			snap := h.provider.DashboardSnap()
			h.mu.Unlock()

			// Store in history ring buffer
			h.historyMu.Lock()
			h.history = append(h.history, snap)
			if len(h.history) > historySize {
				h.history = h.history[len(h.history)-historySize:]
			}
			h.historyMu.Unlock()

			// Broadcast as tick message with backward-compatible format
			tickMsg := map[string]interface{}{
				"type": "tick",
				"data": snap,
			}
			data, err := json.Marshal(tickMsg)
			if err != nil {
				continue
			}
			h.mu.Lock()
			for conn := range h.clients {
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					conn.Close()
					delete(h.clients, conn)
				}
			}
			h.mu.Unlock()
		}
	}()
	log.Println("[DASHBOARD] WebSocket broadcast started (1s interval)")
}
