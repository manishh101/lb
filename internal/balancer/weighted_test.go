package balancer

import (
	"testing"
	"intelligent-lb/internal/metrics"
)

func TestWeightedScore_Select(t *testing.T) {
	ws := WeightedScore{}

	t.Run("Empty candidates returns empty string", func(t *testing.T) {
		result := ws.Select([]string{}, nil, "LOW")
		if result != "" {
			t.Errorf("Expected empty string, got %s", result)
		}
	})

	candidates := []string{"serverA", "serverB"}

	t.Run("HIGH priority prefers fast server", func(t *testing.T) {
		
		// server1 (LOW): 0.6/6 (0.1) + 0.4/11 (0.036) = 0.136
		// server2 (LOW): 0.6/51 (0.011) + 0.4/6 (0.066) = 0.077
		// server1 still wins on LOW here because latency gap is huge. Let's make connections widely different.
		
		stats3 := map[string]metrics.ServerStats{
			"server1": {AvgLatencyMs: 10, ActiveConnections: 100},
			"server2": {AvgLatencyMs: 20, ActiveConnections: 5},
		}
		// HIGH (server1): 0.8/11 (0.072) + 0.2/101 (0.0019) = 0.0739
		// HIGH (server2): 0.8/21 (0.038) + 0.2/6 (0.033) = 0.0713
		// server1 wins HIGH
		
		// LOW (server1): 0.6/11 (0.054) + 0.4/101 (0.0039) = 0.0579
		// LOW (server2): 0.6/21 (0.028) + 0.4/6 (0.066) = 0.094
		// server2 wins LOW
		
		result := ws.Select([]string{"server1", "server2"}, stats3, "HIGH")
		if result != "server1" {
			t.Errorf("Expected server1 (faster) for HIGH priority, got %s", result)
		}
	})

	t.Run("LOW priority prefers idle server", func(t *testing.T) {
		stats3 := map[string]metrics.ServerStats{
			"server1": {AvgLatencyMs: 10, ActiveConnections: 100},
			"server2": {AvgLatencyMs: 20, ActiveConnections: 5},
		}
		result := ws.Select([]string{"server1", "server2"}, stats3, "LOW")
		if result != "server2" {
			t.Errorf("Expected server2 (more idle) for LOW priority, got %s", result)
		}
	})

	t.Run("Missing stats defaults bestScore check correctly", func(t *testing.T) {
		statsMissing := map[string]metrics.ServerStats{
			"serverB": {AvgLatencyMs: 10, ActiveConnections: 10},
		}
		// serverA has no stats
		result := ws.Select(candidates, statsMissing, "LOW")
		if result != "serverB" {
			t.Errorf("Expected serverB to win over absent serverA stats, got %s", result)
		}
	})
}
