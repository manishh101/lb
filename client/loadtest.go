//go:build ignore

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

type result struct {
	latencyMs float64
	status    int
	server    string
	priority  string
}

func main() {
	total := flag.Int("requests", 300, "Total requests")
	concurrency := flag.Int("concurrency", 20, "Concurrent goroutines")
	highPct := flag.Float64("high", 0.2, "Fraction of HIGH priority requests")
	lbURL := flag.String("url", "http://localhost:8080/api/test", "Load balancer URL")
	flag.Parse()

	var results []result
	var mu sync.Mutex
	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup
	testStart := time.Now()

	for i := 0; i < *total; i++ {
		pri := "LOW"
		if float64(i)/float64(*total) < *highPct {
			pri = "HIGH"
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(p string) {
			defer func() { <-sem; wg.Done() }()
			t := time.Now()
			req, _ := http.NewRequest("GET", *lbURL, nil)
			req.Header.Set("X-Priority", p)
			resp, err := http.DefaultClient.Do(req)
			ms := float64(time.Since(t).Milliseconds())
			if err != nil {
				mu.Lock()
				results = append(results, result{ms, 0, "error", p})
				mu.Unlock()
				return
			}
			defer resp.Body.Close()
			srv := resp.Header.Get("X-Handled-By")
			mu.Lock()
			results = append(results, result{ms, resp.StatusCode, srv, p})
			mu.Unlock()
		}(pri)
	}
	wg.Wait()
	elapsed := time.Since(testStart)

	var lats []float64
	ok := 0
	dist := map[string]int{}
	highOK, lowOK := 0, 0
	for _, r := range results {
		if r.status == 200 {
			ok++
			lats = append(lats, r.latencyMs)
			dist[r.server]++
			if r.priority == "HIGH" {
				highOK++
			} else {
				lowOK++
			}
		}
	}
	sort.Float64s(lats)
	avg, p95 := 0.0, 0.0
	if len(lats) > 0 {
		for _, v := range lats {
			avg += v
		}
		avg /= float64(len(lats))
		p95idx := int(float64(len(lats)) * 0.95)
		if p95idx >= len(lats) {
			p95idx = len(lats) - 1
		}
		p95 = lats[p95idx]
	}

	fmt.Printf("\n╔══════════════════════════════════════════╗\n")
	fmt.Printf("║         LOAD TEST RESULTS                ║\n")
	fmt.Printf("╠══════════════════════════════════════════╣\n")
	fmt.Printf("║ Total Requests  : %22d ║\n", *total)
	fmt.Printf("║ Elapsed Time    : %19s ║\n", elapsed.Round(time.Millisecond))
	fmt.Printf("║ Throughput      : %18.1f rps ║\n", float64(*total)/elapsed.Seconds())
	fmt.Printf("║ Success Rate    : %20.1f%% ║\n", float64(ok)/float64(*total)*100)
	fmt.Printf("║ Avg Latency     : %19.1f ms ║\n", avg)
	fmt.Printf("║ P95 Latency     : %19.1f ms ║\n", p95)
	fmt.Printf("║ HIGH Pri OK     : %22d ║\n", highOK)
	fmt.Printf("║ LOW  Pri OK     : %22d ║\n", lowOK)
	fmt.Printf("╠══════════════════════════════════════════╣\n")
	for srv, cnt := range dist {
		pct := float64(cnt) / float64(ok) * 100
		fmt.Printf("║  %-14s  %5d reqs  %5.1f%%    ║\n", srv, cnt, pct)
	}
	fmt.Printf("╚══════════════════════════════════════════╝\n")

	log.Println("[LOAD TEST] Complete")
}
