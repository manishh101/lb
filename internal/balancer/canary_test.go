package balancer

import (
	"math"
	"testing"

	"intelligent-lb/internal/metrics"
)

func TestCanary_SWRR_Distribution(t *testing.T) {
	t.Run("90/10 weight distribution", func(t *testing.T) {
		c := &Canary{}
		candidates := []string{"serverA", "serverB"}
		stats := map[string]metrics.ServerStats{
			"serverA": {Weight: 90, IsHealthy: true},
			"serverB": {Weight: 10, IsHealthy: true},
		}

		counts := map[string]int{"serverA": 0, "serverB": 0}
		totalRuns := 1000

		for i := 0; i < totalRuns; i++ {
			selected := c.Select(candidates, stats, "LOW")
			counts[selected]++
		}

		// serverA should get ~90% (900 out of 1000)
		ratioA := float64(counts["serverA"]) / float64(totalRuns) * 100
		ratioB := float64(counts["serverB"]) / float64(totalRuns) * 100

		if math.Abs(ratioA-90) > 1 {
			t.Errorf("Expected serverA ~90%%, got %.1f%% (%d/%d)", ratioA, counts["serverA"], totalRuns)
		}
		if math.Abs(ratioB-10) > 1 {
			t.Errorf("Expected serverB ~10%%, got %.1f%% (%d/%d)", ratioB, counts["serverB"], totalRuns)
		}
	})

	t.Run("Equal weights", func(t *testing.T) {
		c := &Canary{}
		candidates := []string{"s1", "s2", "s3"}
		stats := map[string]metrics.ServerStats{
			"s1": {Weight: 1, IsHealthy: true},
			"s2": {Weight: 1, IsHealthy: true},
			"s3": {Weight: 1, IsHealthy: true},
		}

		counts := map[string]int{"s1": 0, "s2": 0, "s3": 0}
		totalRuns := 999 // divisible by 3

		for i := 0; i < totalRuns; i++ {
			selected := c.Select(candidates, stats, "LOW")
			counts[selected]++
		}

		for name, count := range counts {
			expected := 333 // 999 / 3
			if math.Abs(float64(count-expected)) > 1 {
				t.Errorf("Expected %s ~%d, got %d", name, expected, count)
			}
		}
	})

	t.Run("Single server gets all traffic", func(t *testing.T) {
		c := &Canary{}
		candidates := []string{"only"}
		stats := map[string]metrics.ServerStats{
			"only": {Weight: 5, IsHealthy: true},
		}

		for i := 0; i < 100; i++ {
			selected := c.Select(candidates, stats, "LOW")
			if selected != "only" {
				t.Fatalf("Expected 'only', got %s", selected)
			}
		}
	})

	t.Run("Empty candidates returns empty", func(t *testing.T) {
		c := &Canary{}
		selected := c.Select(nil, nil, "LOW")
		if selected != "" {
			t.Errorf("Expected empty, got %s", selected)
		}
	})

	t.Run("70/20/10 three-way split", func(t *testing.T) {
		c := &Canary{}
		candidates := []string{"a", "b", "c"}
		stats := map[string]metrics.ServerStats{
			"a": {Weight: 7, IsHealthy: true},
			"b": {Weight: 2, IsHealthy: true},
			"c": {Weight: 1, IsHealthy: true},
		}

		counts := map[string]int{"a": 0, "b": 0, "c": 0}
		totalRuns := 1000

		for i := 0; i < totalRuns; i++ {
			selected := c.Select(candidates, stats, "HIGH")
			counts[selected]++
		}

		ratioA := float64(counts["a"]) / float64(totalRuns) * 100
		ratioB := float64(counts["b"]) / float64(totalRuns) * 100
		ratioC := float64(counts["c"]) / float64(totalRuns) * 100

		if math.Abs(ratioA-70) > 1 {
			t.Errorf("Expected a ~70%%, got %.1f%%", ratioA)
		}
		if math.Abs(ratioB-20) > 1 {
			t.Errorf("Expected b ~20%%, got %.1f%%", ratioB)
		}
		if math.Abs(ratioC-10) > 1 {
			t.Errorf("Expected c ~10%%, got %.1f%%", ratioC)
		}
	})
}
