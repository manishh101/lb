package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"sync/atomic"
	"time"
)

var (
	lbURL      = "http://localhost:8080/api/chaos"
	serverURLs = []string{
		"http://localhost:8001/toggle", // Alpha
		"http://localhost:8002/toggle", // Beta
		"http://localhost:8003/toggle", // Gamma
		"http://localhost:8004/toggle", // Delta
	}
	serverNames = []string{"Alpha", "Beta", "Gamma", "Delta"}
)

func main() {
	fmt.Println("🌪️ Starting Chaos & Stress Test...")

	// 1. High Concurrency Stress Generator
	// We use 50 concurrent workers continuously sending requests to keep "Active Connections" visibly > 0
	go stressTest(50)

	// 2. Chaos Monkey
	// Randomly toggle servers every 12 seconds
	chaosMonkey(12 * time.Second)
}

func stressTest(concurrency int) {
	var totalReqs atomic.Int64
	var failedReqs atomic.Int64

	fmt.Printf("🔥 Launching %d concurrent stress workers...\n", concurrency)
	
	for i := 0; i < concurrency; i++ {
		go func() {
			client := &http.Client{Timeout: 5 * time.Second}
			for {
				req, _ := http.NewRequest("GET", lbURL, nil)
				// Random priority 25% HIGH
				if rand.Intn(4) == 0 {
					req.Header.Set("X-Priority", "HIGH")
				}
				
				resp, err := client.Do(req)
				if err != nil || resp.StatusCode >= 500 {
					failedReqs.Add(1)
				}
				if resp != nil && resp.Body != nil {
					resp.Body.Close()
				}
				totalReqs.Add(1)

				// Small delay to prevent complete client-side CPU exhaustion
				// varying between 10ms and 50ms per request per worker
				time.Sleep(time.Duration(10+rand.Intn(40)) * time.Millisecond)
			}
		}()
	}

	for {
		time.Sleep(5 * time.Second)
		total := totalReqs.Load()
		failed := failedReqs.Load()
		fmt.Printf("[STRESS] Sent %d requests so far (%d failures)\n", total, failed)
	}
}

func chaosMonkey(interval time.Duration) {
	serverStates := []bool{true, true, true, true} // true = healthy

	for {
		time.Sleep(interval)

		// Pick a random server
		idx := rand.Intn(len(serverURLs))
		target := serverURLs[idx]
		name := serverNames[idx]

		// Toggle its state
		resp, err := http.Get(target)
		if err == nil {
			resp.Body.Close()
			serverStates[idx] = !serverStates[idx]
			
			stateStr := "HEALTHY"
			if !serverStates[idx] {
				stateStr = "FAILING (HTTP 500)"
			}
			
			fmt.Printf("\n🐒 [CHAOS] Toggled %s -> currently %s\n\n", name, stateStr)
		} else {
			fmt.Printf("🐒 [CHAOS] Failed to toggle %s: %v\n", name, err)
		}
	}
}
