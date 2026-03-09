package config

import (
	"encoding/json"
	"os"
)

// ServerConfig holds configuration for a single backend server.
type ServerConfig struct {
	URL     string `json:"url"`
	Name    string `json:"name"`
	Weight  int    `json:"weight"`
	DelayMs int    `json:"delay_ms"`
}

// Config holds the entire load balancer configuration.
type Config struct {
	ListenPort         int            `json:"listen_port"`
	DashboardPort      int            `json:"dashboard_port"`
	Servers            []ServerConfig `json:"servers"`
	Algorithm          string         `json:"algorithm"`
	HealthInterval     int            `json:"health_interval_sec"`
	BreakerThreshold   int            `json:"breaker_threshold"`
	BreakerTimeoutSec  int            `json:"breaker_timeout_sec"`
	MetricsIntervalSec int            `json:"metrics_interval_sec"`
}

// Load reads and parses a JSON configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// Set defaults
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 8080
	}
	if cfg.DashboardPort == 0 {
		cfg.DashboardPort = 8081
	}
	if cfg.HealthInterval == 0 {
		cfg.HealthInterval = 5
	}
	if cfg.BreakerThreshold == 0 {
		cfg.BreakerThreshold = 3
	}
	if cfg.BreakerTimeoutSec == 0 {
		cfg.BreakerTimeoutSec = 15
	}
	if cfg.MetricsIntervalSec == 0 {
		cfg.MetricsIntervalSec = 10
	}
	if cfg.Algorithm == "" {
		cfg.Algorithm = "weighted"
	}
	return &cfg, nil
}
