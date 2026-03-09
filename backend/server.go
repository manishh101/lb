package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

var (
	port       = 8001
	delayMs    = 10
	serverName = "Server"
	reqCount   atomic.Int64 // FIX B4: thread-safe counter
)

func main() {
	if len(os.Args) > 1 {
		port, _ = strconv.Atoi(os.Args[1])
	}
	if len(os.Args) > 2 {
		delayMs, _ = strconv.Atoi(os.Args[2])
	}
	if len(os.Args) > 3 {
		serverName = os.Args[3]
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/", apiHandler)

	log.Printf("[%s] Starting on :%d (base delay: %dms)", serverName, port, delayMs)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "UP",
		"port":            port,
		"name":            serverName,
		"requests_served": reqCount.Load(),
	})
}

func apiHandler(w http.ResponseWriter, r *http.Request) {
	jitter := 0
	if delayMs > 0 {
		jitter = rand.Intn(delayMs/2 + 1)
	}
	time.Sleep(time.Duration(delayMs+jitter) * time.Millisecond)
	count := reqCount.Add(1) // FIX B4: atomic increment

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Server-Name", serverName) // for tracing
	json.NewEncoder(w).Encode(map[string]interface{}{
		"handled_by":    serverName,
		"port":          port,
		"request_count": count,
		"delay_ms":      delayMs + jitter,
		"path":          r.URL.Path,
	})
}
