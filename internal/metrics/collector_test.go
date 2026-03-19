package metrics

import (
	"math"
	"testing"
)

func TestP95Calculation(t *testing.T) {
	t.Run("Empty latencies returns 0", func(t *testing.T) {
		result := computeP95(nil)
		if result != 0 {
			t.Errorf("Expected 0, got %f", result)
		}
	})

	t.Run("Single value returns that value", func(t *testing.T) {
		result := computeP95([]float64{42.0})
		if result != 42.0 {
			t.Errorf("Expected 42.0, got %f", result)
		}
	})

	t.Run("Known distribution P95 is 95th percentile", func(t *testing.T) {
		// 100 values from 1.0 to 100.0
		latencies := make([]float64, 100)
		for i := 0; i < 100; i++ {
			latencies[i] = float64(i + 1)
		}
		result := computeP95(latencies)
		// P95 of [1..100] at index int(math.Ceil(0.95*100))-1=94 -> value 95.0
		if result != 95.0 {
			t.Errorf("Expected 95.0 (P95 of 1-100), got %f", result)
		}
	})

	t.Run("20 values P95", func(t *testing.T) {
		// 20 values: 1..20
		latencies := make([]float64, 20)
		for i := 0; i < 20; i++ {
			latencies[i] = float64(i + 1)
		}
		result := computeP95(latencies)
		// index = int(math.Ceil(0.95 * 20))-1 = 19 - 1 = 18 -> value 19.0
		if result != 19.0 {
			t.Errorf("Expected 19.0, got %f", result)
		}
	})

	t.Run("P95 is not affected by original order", func(t *testing.T) {
		// Deliberately unsorted: the 95th percentile should still work
		latencies := []float64{50, 10, 90, 5, 95, 1, 100, 20, 30, 70}
		result := computeP95(latencies)
		// sorted: [1, 5, 10, 20, 30, 50, 70, 90, 95, 100]
		// index = int(0.95 * 10) = 9 -> value 100
		if result != 100.0 {
			t.Errorf("Expected 100.0, got %f", result)
		}
	})
}

func TestRetryCounter(t *testing.T) {
	servers := []string{"http://alpha", "http://beta"}
	names := []string{"Alpha", "Beta"}
	c := New(servers, names, nil)

	c.RecordRetry("http://alpha")
	c.RecordRetry("http://alpha")
	c.RecordRetry("http://beta")

	snap := c.Snapshot()
	if snap["http://alpha"].RetryCount != 2 {
		t.Errorf("Expected Alpha RetryCount=2, got %d", snap["http://alpha"].RetryCount)
	}
	if snap["http://beta"].RetryCount != 1 {
		t.Errorf("Expected Beta RetryCount=1, got %d", snap["http://beta"].RetryCount)
	}
}

func TestCircuitEvents(t *testing.T) {
	servers := []string{"http://alpha"}
	names := []string{"Alpha"}
	c := New(servers, names, nil)

	// Initial state is CLOSED, set to OPEN -> event recorded
	c.SetCircuitState("http://alpha", "OPEN")
	events := c.CircuitEvents()
	if len(events) != 1 {
		t.Fatalf("Expected 1 circuit event, got %d", len(events))
	}
	if events[0].OldState != "CLOSED" || events[0].NewState != "OPEN" {
		t.Errorf("Expected CLOSED->OPEN, got %s->%s", events[0].OldState, events[0].NewState)
	}

	// Same state -> no new event
	c.SetCircuitState("http://alpha", "OPEN")
	events = c.CircuitEvents()
	if len(events) != 1 {
		t.Errorf("Expected 1 event (no change), got %d", len(events))
	}

	// OPEN -> HALF_OPEN -> event recorded
	c.SetCircuitState("http://alpha", "HALF_OPEN")
	events = c.CircuitEvents()
	if len(events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(events))
	}
}

func TestRPSComputation(t *testing.T) {
	servers := []string{"http://alpha"}
	names := []string{"Alpha"}
	c := New(servers, names, nil)

	// Record some requests
	for i := 0; i < 10; i++ {
		c.RecordStart("http://alpha")
		c.RecordEnd("http://alpha", 5.0, true)
	}

	snap := c.DashboardSnap()
	// RPS should be > 0 since we recorded 10 requests
	if snap.TotalRequests != 10 {
		t.Errorf("Expected 10 total requests, got %d", snap.TotalRequests)
	}
	// RPS is computed from delta, may be very high since time is small
	if math.IsNaN(snap.GlobalRPS) || math.IsInf(snap.GlobalRPS, 0) {
		t.Errorf("GlobalRPS should be a valid number, got %f", snap.GlobalRPS)
	}
}

func TestP95InServerStats(t *testing.T) {
	servers := []string{"http://alpha"}
	names := []string{"Alpha"}
	c := New(servers, names, nil)

	// Record 100 requests with known latencies
	for i := 1; i <= 100; i++ {
		c.RecordStart("http://alpha")
		c.RecordEnd("http://alpha", float64(i), true)
	}

	snap := c.Snapshot()
	p95 := snap["http://alpha"].P95LatencyMs
	// Should be approximately 96 (95th percentile of 1-100)
	if p95 < 95 || p95 > 100 {
		t.Errorf("Expected P95 around 96, got %f", p95)
	}
}
