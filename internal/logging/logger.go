package logging

import (
	"encoding/json"
	"log"
	"os"
	"time"
)

// defaultLogger writes to stdout without standard log prefixes,
// as the JSON itself handles the timestamp formatting.
var defaultLogger = log.New(os.Stdout, "", 0)

// AccessLog represents a structured JSON log entry for load balancer access logs.
type AccessLog struct {
	Time       string  `json:"time"`
	Level      string  `json:"level"`
	Message    string  `json:"msg"`
	Method     string  `json:"method,omitempty"`
	Path       string  `json:"path,omitempty"`
	Priority   string  `json:"priority,omitempty"`
	ServerName string  `json:"server_name,omitempty"`
	Target     string  `json:"target,omitempty"`
	StatusCode int     `json:"status_code,omitempty"`
	LatencyMs  float64 `json:"latency_ms,omitempty"`
	Attempt    int     `json:"attempt,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// Info logs a structured informational entry.
func Info(entry AccessLog) {
	entry.Time = time.Now().UTC().Format(time.RFC3339Nano)
	if entry.Level == "" {
		entry.Level = "INFO"
	}
	b, _ := json.Marshal(entry)
	defaultLogger.Println(string(b))
}

// Error logs a structured error entry.
func Error(entry AccessLog) {
	entry.Time = time.Now().UTC().Format(time.RFC3339Nano)
	entry.Level = "ERROR"
	b, _ := json.Marshal(entry)
	defaultLogger.Println(string(b))
}
