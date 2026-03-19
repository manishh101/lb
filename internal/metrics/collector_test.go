package metrics

import (
	"testing"
)

func TestCollector(t *testing.T) {
	servers := []string{"http://alpha", "http://beta"}
	names := []string{"Alpha", "Beta"}
	
	c := New(servers, names, nil)
	
	t.Run("Initial State", func(t *testing.T) {
		snap := c.Snapshot()
		if len(snap) != 2 {
			t.Fatalf("Expected 2 servers in snapshot, got %d", len(snap))
		}
		
		alphaStats := snap["http://alpha"]
		if alphaStats.Name != "Alpha" || !alphaStats.IsHealthy || alphaStats.CircuitState != "CLOSED" {
			t.Errorf("Initial state of alpha is incorrect: %+v", alphaStats)
		}
	})
	
	t.Run("Record connections and latency", func(t *testing.T) {
		c.RecordStart("http://alpha") // +1 conn
		c.RecordStart("http://alpha") // 2 conns
		
		snap := c.Snapshot()
		if snap["http://alpha"].ActiveConnections != 2 {
			t.Errorf("Expected 2 active connections, got %d", snap["http://alpha"].ActiveConnections)
		}
		
		// End 1 request, successful, 10ms latency
		c.RecordEnd("http://alpha", 10.0, true)
		
		snap = c.Snapshot()
		a := snap["http://alpha"]
		if a.ActiveConnections != 1 {
			t.Errorf("Expected 1 active connection, got %d", a.ActiveConnections)
		}
		if a.AvgLatencyMs != 10.0 {
			t.Errorf("Expected 10.0 avg latency, got %f", a.AvgLatencyMs)
		}
		if a.TotalRequests != 1 || a.SuccessCount != 1 || a.FailureCount != 0 {
			t.Errorf("Incorrect counts after success")
		}
		
		// End 1 request, failed, 20ms latency
		c.RecordEnd("http://alpha", 20.0, false)
		
		snap = c.Snapshot()
		a = snap["http://alpha"]
		if a.ActiveConnections != 0 {
			t.Errorf("Expected 0 active connections, got %d", a.ActiveConnections)
		}
		if a.AvgLatencyMs != 15.0 {
			t.Errorf("Expected 15.0 avg latency, got %f", a.AvgLatencyMs)
		}
		if a.TotalRequests != 2 || a.SuccessCount != 1 || a.FailureCount != 1 {
			t.Errorf("Incorrect counts after failure")
		}
	})
	
	t.Run("Health and Circuit State", func(t *testing.T) {
		c.SetHealth("http://beta", false)
		c.SetCircuitState("http://beta", "OPEN")
		
		snap := c.Snapshot()
		b := snap["http://beta"]
		if b.IsHealthy {
			t.Errorf("Expected beta to be unhealthy")
		}
		if b.CircuitState != "OPEN" {
			t.Errorf("Expected beta circuit to be OPEN")
		}
	})
	
	t.Run("Record Priority", func(t *testing.T) {
		c.RecordPriority("http://alpha", "HIGH")
		c.RecordPriority("http://alpha", "LOW")
		c.RecordPriority("http://alpha", "LOW")
		
		snap := c.Snapshot()
		a := snap["http://alpha"]
		if a.HighPriorityCount != 1 || a.LowPriorityCount != 2 {
			t.Errorf("Incorrect priority counts: HIGH=%d, LOW=%d", a.HighPriorityCount, a.LowPriorityCount)
		}
	})
}
