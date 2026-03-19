package metrics

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// StartReporter launches a background goroutine that prints a formatted
// metrics table to the terminal at the specified interval.
func (c *Collector) StartReporter(intervalSec int) {
	go func() {
		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		for range ticker.C {
			c.PrintReport()
		}
	}()
	log.Println("[REPORTER] Terminal metrics table started")
}

// PrintReport outputs a single metrics snapshot to stdout.
func (c *Collector) PrintReport() {
	snap := c.DashboardSnap()
	line := strings.Repeat("─", 110)
	fmt.Println("\n" + line)
	fmt.Printf("  %-12s %-8s %-9s %-9s %-9s %-8s %-8s %-10s %s\n",
		"Server", "Health", "Requests", "Avg(ms)", "P95(ms)", "Active", "Retries", "Success%", "Circuit")
	fmt.Println(line)
	for _, s := range snap.Servers {
		health := "UP  ✓"
		if !s.IsHealthy {
			health = "DOWN ✗"
		}
		rate := 0.0
		if s.TotalRequests > 0 {
			rate = float64(s.SuccessCount) / float64(s.TotalRequests) * 100
		}
		fmt.Printf("  %-12s %-8s %-9d %-9.1f %-9.1f %-8d %-8d %-10.1f %s\n",
			s.Name, health, s.TotalRequests, s.AvgLatencyMs, s.P95LatencyMs,
			s.ActiveConnections, s.RetryCount, rate, s.CircuitState)
	}
	fmt.Println(line)
	fmt.Printf("  TOTAL: %d requests  |  Success: %.1f%%  |  RPS: %.1f  |  Healthy: %d/%d  |  %s\n",
		snap.TotalRequests, snap.SuccessRate, snap.GlobalRPS,
		snap.HealthyCount, snap.TotalCount, time.Now().Format("15:04:05"))
	fmt.Println(line)
}
