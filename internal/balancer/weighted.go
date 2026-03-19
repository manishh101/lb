package balancer

import (
	"math/rand"
	"time"

	"intelligent-lb/internal/metrics"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

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
		// Base statistical score (Latency & Load)
		baseScore := latencyWeight/(1.0+s.AvgLatencyMs) + loadWeight/(1.0+float64(s.ActiveConnections))
		
		// Gap 4: Incorporate Configured Server Weight multiplier
		weightMultiplier := float64(s.Weight)
		if weightMultiplier <= 0 {
			weightMultiplier = 1.0
		}
		
		// Gap 5: Jitter (Tiny random float [0.0, 0.001)) to break 100% ties at startup
		jitter := rand.Float64() * 0.001

		score := (baseScore * weightMultiplier) + jitter
		if score > bestScore {
			bestScore = score
			bestServer = url
		}
	}
	return bestServer
}
