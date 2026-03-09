package balancer

import (
	"intelligent-lb/internal/metrics"
)

// Algorithm defines the interface for all load balancing algorithms.
// Each algorithm selects one server from the candidates based on stats and priority.
type Algorithm interface {
	Select(candidates []string, stats map[string]metrics.ServerStats, priority string) string
}

// WeightedScore implements intelligent routing using real-time latency and load metrics.
// HIGH priority requests favour latency (0.8 weight), LOW priority balances more evenly (0.6).
type WeightedScore struct{}

// Select picks the server with the highest weighted score.
// Score = latencyWeight/(1+avgLatency) + loadWeight/(1+activeConnections)
func (ws WeightedScore) Select(
	candidates []string,
	stats map[string]metrics.ServerStats,
	priority string,
) string {
	if len(candidates) == 0 {
		return ""
	}

	// Priority-dependent weights
	latencyWeight := 0.6
	loadWeight := 0.4
	if priority == "HIGH" {
		latencyWeight = 0.8
		loadWeight = 0.2
	}

	bestScore := -1.0
	bestServer := candidates[0]

	for _, url := range candidates {
		s, ok := stats[url]
		if !ok {
			continue
		}
		score := latencyWeight/(1.0+s.AvgLatencyMs) + loadWeight/(1.0+float64(s.ActiveConnections))
		if score > bestScore {
			bestScore = score
			bestServer = url
		}
	}
	return bestServer
}
