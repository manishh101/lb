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

// Hub manages WebSocket connections and broadcasts metrics to all clients.
type Hub struct {
	metrics  *metrics.Collector
	clients  map[*websocket.Conn]bool
	mu       sync.Mutex
	htmlPath string
}

// NewHub creates a new dashboard hub.
func NewHub(m *metrics.Collector, htmlPath string) *Hub {
	return &Hub{
		metrics:  m,
		clients:  make(map[*websocket.Conn]bool),
		htmlPath: htmlPath,
	}
}

// ServeHTTP serves the dashboard HTML page.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, h.htmlPath)
}

// HandleWS upgrades HTTP connections to WebSocket and registers clients.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[DASHBOARD] WebSocket upgrade failed: %v", err)
		return
	}
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

// StartBroadcast sends metrics snapshots to all connected WebSocket clients
// at 1-second intervals.
func (h *Hub) StartBroadcast() {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		for range ticker.C {
			snap := h.metrics.Snapshot()
			data, err := json.Marshal(snap)
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
