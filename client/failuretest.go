//go:build ignore

package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

// FailureRecoveryTest continuously sends requests and measures:
// 1. How long until failure is detected (first non-200 response)
// 2. How long until recovery completes (first 200 after failures)
// Run this, then manually kill a backend server, then restart it.
func main() {
	lbURL := "http://localhost:8080/api/test"
	client := &http.Client{Timeout: 3 * time.Second}

	var (
		failureDetectedAt time.Time
		failureStarted    bool
		lastStatus        int
	)

	fmt.Println("[FAILURE TEST] Sending requests every 500ms...")
	fmt.Println("[FAILURE TEST] Kill a backend server now to see detection + recovery time.")
	fmt.Println("[FAILURE TEST] Restart the server to see self-healing measurement.")
	fmt.Println("")

	for i := 0; ; i++ {
		start := time.Now()
		req, _ := http.NewRequest("GET", lbURL, nil)
		req.Header.Set("X-Priority", "LOW")
		resp, err := client.Do(req)
		ms := float64(time.Since(start).Milliseconds())

		status := 0
		server := ""
		if err == nil {
			status = resp.StatusCode
			server = resp.Header.Get("X-Handled-By")
			resp.Body.Close()
		}

		// Detect failure onset
		if status != 200 && !failureStarted {
			failureStarted = true
			failureDetectedAt = time.Now()
			log.Printf("[FAILURE DETECTED] Request #%d failed (status %d) at %s",
				i, status, failureDetectedAt.Format("15:04:05.000"))
		}

		// Detect recovery — measures actual recovery time
		if status == 200 && failureStarted && lastStatus != 200 {
			recoveryTime := time.Since(failureDetectedAt)
			log.Printf("[RECOVERY COMPLETE] First successful response after failure!")
			log.Printf("[RECOVERY COMPLETE] Server: %s | Recovery time: %s",
				server, recoveryTime.Round(time.Millisecond))
			failureStarted = false
		}

		lastStatus = status
		log.Printf("[REQUEST #%4d] status=%d  server=%-10s  latency=%.0fms",
			i, status, server, ms)
		time.Sleep(500 * time.Millisecond)
	}
}
