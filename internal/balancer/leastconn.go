package balancer

import "intelligent-lb/internal/metrics"

// LeastConnections routes to the server with the fewest active connections.
// Better than RoundRobin when requests have highly variable processing time.
type LeastConnections struct{}

// Select picks the server with the minimum active connection count.
func (lc LeastConnections) Select(
	candidates []string,
	stats map[string]metrics.ServerStats,
	priority string,
) string {
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	bestConn := int64(1 << 62) // max int64
	for _, url := range candidates {
		s, ok := stats[url]
		if !ok {
			continue
		}
		if s.ActiveConnections < bestConn {
			bestConn = s.ActiveConnections
			best = url
		}
	}
	return best
}
