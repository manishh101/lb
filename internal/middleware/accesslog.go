package middleware

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// accessLogEntry represents a single JSON line in the access log.
type accessLogEntry struct {
	Timestamp   string  `json:"timestamp"`
	RequestID   string  `json:"request_id,omitempty"`
	ClientIP    string  `json:"client_ip,omitempty"`
	Method      string  `json:"method"`
	Path        string  `json:"path"`
	Priority    string  `json:"priority,omitempty"`
	BackendName string  `json:"backend_name,omitempty"`
	BackendURL  string  `json:"backend_url,omitempty"`
	StatusCode  int     `json:"status_code"`
	LatencyMs   float64 `json:"latency_ms"`
	Attempts    int     `json:"attempts,omitempty"`
	BytesSent   int     `json:"bytes_sent"`
}

// statusRecorder wraps http.ResponseWriter to capture the status code and bytes written.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
	bytes      int
	written    bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.written {
		sr.statusCode = code
		sr.written = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.written {
		sr.statusCode = http.StatusOK
		sr.written = true
	}
	n, err := sr.ResponseWriter.Write(b)
	sr.bytes += n
	return n, err
}

// accessLogger holds the file logger for access logs.
type accessLogger struct {
	mu     sync.RWMutex
	logger *log.Logger
}

// NewAccessLog creates a middleware that logs every request as a single JSON line
// to the specified file path. Also logs to terminal in a clean format.
// Inspired by Traefik's accesslog middleware.
func NewAccessLog(filePath string) Middleware {
	al := &accessLogger{}

	if filePath != "" {
		f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("[ACCESS-LOG] Warning: failed to open log file %s: %v", filePath, err)
		} else {
			al.logger = log.New(f, "", 0)
			log.Printf("[ACCESS-LOG] Writing access logs to %s", filePath)
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			recorder := &statusRecorder{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			next.ServeHTTP(recorder, r)

			latency := time.Since(start)

			entry := accessLogEntry{
				Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
				RequestID:   RequestIDFromContext(r.Context()),
				ClientIP:    extractIP(r),
				Method:      r.Method,
				Path:        r.URL.Path,
				Priority:    r.Header.Get("X-Priority"),
				BackendName: recorder.Header().Get("X-Handled-By"),
				BackendURL:  recorder.Header().Get("X-Backend-URL"),
				StatusCode:  recorder.statusCode,
				LatencyMs:   float64(latency.Microseconds()) / 1000.0,
				BytesSent:   recorder.bytes,
			}

			// Parse X-Attempts if set by retry middleware
			if attemptsStr := recorder.Header().Get("X-Attempts"); attemptsStr != "" {
				var attempts int
				if _, err := fmt.Sscanf(attemptsStr, "%d", &attempts); err == nil {
					entry.Attempts = attempts
				}
			}

			b, _ := json.Marshal(entry)
			line := string(b)

			// Write to file
			al.mu.RLock()
			fl := al.logger
			al.mu.RUnlock()
			if fl != nil {
				fl.Println(line)
			}

			// Clean terminal log
			log.Printf("[ACCESS] %s %s %d %s %.2fms %dB",
				entry.ClientIP, entry.Method, entry.StatusCode,
				entry.Path, entry.LatencyMs, entry.BytesSent)
		})
	}
}
