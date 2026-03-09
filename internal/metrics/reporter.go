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
	snap := c.Snapshot()
	line := strings.Repeat("─", 80)
	fmt.Println("\n" + line)
	fmt.Printf("  %-12s %-8s %-10s %-10s %-8s %-10s %s\n",
		"Server", "Health", "Requests", "Avg(ms)", "Active", "Success%", "Circuit")
	fmt.Println(line)
	var totalReq, totalOK int64
	for _, s := range snap {
		health := "UP  ✓"
		if !s.IsHealthy {
			health = "DOWN ✗"
		}
		rate := 0.0
		if s.TotalRequests > 0 {
			rate = float64(s.SuccessCount) / float64(s.TotalRequests) * 100
		}
		fmt.Printf("  %-12s %-8s %-10d %-10.1f %-8d %-10.1f %s\n",
			s.Name, health, s.TotalRequests, s.AvgLatencyMs,
			s.ActiveConnections, rate, s.CircuitState)
		totalReq += s.TotalRequests
		totalOK += s.SuccessCount
	}
	fmt.Println(line)
	globalRate := 0.0
	if totalReq > 0 {
		globalRate = float64(totalOK) / float64(totalReq) * 100
	}
	fmt.Printf("  TOTAL: %d requests  |  Success: %.1f%%  |  %s\n",
		totalReq, globalRate, time.Now().Format("15:04:05"))
	fmt.Println(line)
}
