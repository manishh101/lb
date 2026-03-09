package main

import (
	"fmt"
	"math"
	"net/http"
	"time"
)

func main() {
	lbURL := "http://localhost:8080/api/dynamic"
	fmt.Println("🚀 Starting dynamic load generator...")
	fmt.Println("Config: Oscillating traffic between 5 and 30 requests per second.")

	start := time.Now()
	for {
		// Calculate current RPS using a sine wave for oscillation
		// Period of 30 seconds
		elapsed := time.Since(start).Seconds()
		sinVal := math.Sin(elapsed * (2 * math.Pi / 30))
		rps := 15 + (int(sinVal * 10)) // Oscillate between 5 and 25 RPS

		for i := 0; i < rps; i++ {
			go func() {
				req, _ := http.NewRequest("GET", lbURL, nil)
				
				// Randomly assign HIGH priority (25% chance)
				if math.Remainder(float64(time.Now().UnixNano()), 4) == 0 {
					req.Header.Set("X-Priority", "HIGH")
				}
				
				resp, err := http.DefaultClient.Do(req)
				if (err == nil) {
					resp.Body.Close()
				}
			}()
		}

		fmt.Printf("[%s] Target RPS: %d\n", time.Now().Format("15:04:05"), rps)
		time.Sleep(1 * time.Second)
	}
}
