package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// HealthCheckConfig holds per-server or per-service health check settings.
// All fields are optional — sensible defaults are applied from the global config.
type HealthCheckConfig struct {
	Path           string `json:"path,omitempty"`            // Health check endpoint path (default: "/health")
	IntervalSec    int    `json:"interval_sec,omitempty"`    // Check interval in seconds (default: global health_interval_sec)
	TimeoutSec     int    `json:"timeout_sec,omitempty"`     // HTTP timeout for health check (default: 2)
	ExpectedStatus int    `json:"expected_status,omitempty"` // Expected HTTP status code (default: 200)
}

// CircuitBreakerConfig holds per-service circuit breaker settings.
type CircuitBreakerConfig struct {
	Threshold          int `json:"threshold"`            // Failures before tripping (default: global breaker_threshold)
	RecoveryTimeoutSec int `json:"recovery_timeout_sec"` // Seconds before probe attempt (default: global breaker_timeout_sec)
}

// LoadBalancerConfig holds per-service load balancer settings.
type LoadBalancerConfig struct {
	Algorithm string `json:"algorithm,omitempty"` // "weighted", "roundrobin", "leastconn", "canary"
	Sticky    bool   `json:"sticky,omitempty"`
}

// ServerConfig holds configuration for a single backend server.
type ServerConfig struct {
	URL         string            `json:"url"`
	Name        string            `json:"name"`
	Weight      int               `json:"weight"`
	DelayMs     int               `json:"delay_ms"`
	HealthCheck HealthCheckConfig `json:"health_check,omitempty"` // Per-server health check config
}

// RouterConfig holds configuration for a single rule-based router.
type RouterConfig struct {
	Rule        string   `json:"rule"`
	Priority    int      `json:"priority"`
	Middlewares []string `json:"middlewares,omitempty"`
	Service     string   `json:"service"`
}

// ServiceConfig holds configuration for a named backend service.
// Each service has its own backend pool with its own configuration for
// load balancing, health checks, and circuit breakers.
// Inspired by Traefik's service configuration in pkg/config/dynamic/config.go.
type ServiceConfig struct {
	LoadBalancer   *LoadBalancerConfig   `json:"load_balancer,omitempty"`
	HealthCheck    *HealthCheckConfig    `json:"health_check,omitempty"`
	CircuitBreaker *CircuitBreakerConfig `json:"circuit_breaker,omitempty"`
	Canary         bool                  `json:"canary,omitempty"`
	Servers        []ServerConfig        `json:"servers"`
}

// DashboardAuth holds basic authentication credentials for the dashboard.
// If both fields are empty, authentication is disabled (backward compatible).
type DashboardAuth struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// MiddlewareConfig holds the configuration for a single named middleware instance.
// The Type field determines which middleware to instantiate, and Config holds
// the type-specific configuration as raw JSON for deferred parsing.
// Inspired by Traefik's dynamic.Middleware configuration.
type MiddlewareConfig struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// TimeoutConfig holds priority-aware timeout settings.
// Each priority level gets its own HTTP client timeout.
type TimeoutConfig struct {
	HighSec   int `json:"high_sec,omitempty"`
	MediumSec int `json:"medium_sec,omitempty"`
	LowSec    int `json:"low_sec,omitempty"`
}

// TLSConfig holds TLS/HTTPS configuration.
// If Enabled is false (default), the load balancer serves plain HTTP.
type TLSConfig struct {
	Enabled      bool   `json:"enabled,omitempty"`
	CertFile     string `json:"cert_file,omitempty"`
	KeyFile      string `json:"key_file,omitempty"`
	AutoGenerate bool   `json:"auto_generate,omitempty"` // Generate self-signed cert if files don't exist
}

// EntryPointConfig defines a single named entrypoint.
// Each entrypoint runs as its own independent HTTP server with its own
// goroutine, middleware chain, and connection handling.
// Inspired by Traefik's EntryPoint configuration in pkg/config/static/entrypoints.go.
type EntryPointConfig struct {
	Address     string     `json:"address"`               // Listen address, e.g. ":8080"
	Protocol    string     `json:"protocol,omitempty"`     // "http" (default), "https"
	Middlewares []string   `json:"middlewares,omitempty"`  // Entrypoint-level middleware names
	TLS         *TLSConfig `json:"tls,omitempty"`          // Optional per-entrypoint TLS config
}

// CORSConfig holds CORS configuration for the middleware.
type CORSConfig struct {
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
	AllowedMethods []string `json:"allowed_methods,omitempty"`
	AllowedHeaders []string `json:"allowed_headers,omitempty"`
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
	MaxRetries         int            `json:"max_retries"`
	ShutdownTimeoutSec int            `json:"shutdown_timeout_sec"`
	RateLimitRPS       float64        `json:"rate_limit_rps"`
	RateLimitBurst     int            `json:"rate_limit_burst"`
	PerAttemptTimeoutSec int          `json:"per_attempt_timeout_sec"`

	// New fields — all backward compatible with zero-value defaults
	RetryBackoffMs    int           `json:"retry_backoff_ms,omitempty"`
	RetryBackoffMaxMs int           `json:"retry_backoff_max_ms,omitempty"`
	AccessLogPath     string        `json:"access_log_path,omitempty"`
	DashboardAuth     DashboardAuth `json:"dashboard_auth,omitempty"`
	TLS               TLSConfig     `json:"tls,omitempty"`
	CORS              CORSConfig    `json:"cors,omitempty"`
	HotReload         bool          `json:"hot_reload,omitempty"`

	// Middlewares defines named middleware instances with type-specific config.
	Middlewares map[string]*MiddlewareConfig `json:"middlewares,omitempty"`

	// Timeouts defines priority-aware request timeout settings.
	Timeouts TimeoutConfig `json:"timeouts,omitempty"`

	// EntryPoints defines named entrypoints, each running as an independent server.
	EntryPoints map[string]*EntryPointConfig `json:"entrypoints,omitempty"`

	// Routers define rule-based request routing.
	Routers map[string]*RouterConfig `json:"routers,omitempty"`

	// Services define named groups of backend servers with per-service config.
	// Each service can have its own load_balancer, health_check, circuit_breaker,
	// and canary settings. Inspired by Traefik's service configuration.
	Services map[string]*ServiceConfig `json:"services,omitempty"`
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
	setDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// setDefaults applies sensible defaults for any unset config values.
func setDefaults(cfg *Config) {
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
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.ShutdownTimeoutSec == 0 {
		cfg.ShutdownTimeoutSec = 15
	}
	if cfg.RateLimitRPS == 0 {
		cfg.RateLimitRPS = 100
	}
	if cfg.RateLimitBurst == 0 {
		cfg.RateLimitBurst = 200
	}
	if cfg.PerAttemptTimeoutSec == 0 {
		cfg.PerAttemptTimeoutSec = 5
	}
	if cfg.RetryBackoffMs == 0 {
		cfg.RetryBackoffMs = 100
	}
	if cfg.RetryBackoffMaxMs == 0 {
		cfg.RetryBackoffMaxMs = 5000
	}
	if cfg.AccessLogPath == "" {
		cfg.AccessLogPath = "access.log"
	}

	// Priority-aware timeout defaults
	if cfg.Timeouts.HighSec == 0 {
		cfg.Timeouts.HighSec = 5
	}
	if cfg.Timeouts.MediumSec == 0 {
		cfg.Timeouts.MediumSec = 10
	}
	if cfg.Timeouts.LowSec == 0 {
		cfg.Timeouts.LowSec = 20
	}

	// Ensure access log directory exists
	if cfg.AccessLogPath != "" {
		dir := filepath.Dir(cfg.AccessLogPath)
		if dir != "." && dir != "" {
			_ = os.MkdirAll(dir, 0755)
		}
	}

	// Apply per-server health check defaults for legacy global servers
	for i := range cfg.Servers {
		s := &cfg.Servers[i]
		if s.HealthCheck.Path == "" {
			s.HealthCheck.Path = "/health"
		}
		if s.HealthCheck.IntervalSec == 0 {
			s.HealthCheck.IntervalSec = cfg.HealthInterval
		}
		if s.HealthCheck.TimeoutSec == 0 {
			s.HealthCheck.TimeoutSec = 2
		}
		if s.HealthCheck.ExpectedStatus == 0 {
			s.HealthCheck.ExpectedStatus = 200
		}
	}

	// Backward compatibility: if no services defined but flat servers list exists,
	// wrap into a "default" service. This ensures old configs keep working.
	if len(cfg.Services) == 0 && len(cfg.Servers) > 0 {
		cfg.Services = map[string]*ServiceConfig{
			"default": {
				Servers: cfg.Servers,
			},
		}
	}

	// Apply per-service defaults
	for _, svc := range cfg.Services {
		// Service-level health check defaults
		if svc.HealthCheck == nil {
			svc.HealthCheck = &HealthCheckConfig{}
		}
		if svc.HealthCheck.Path == "" {
			svc.HealthCheck.Path = "/health"
		}
		if svc.HealthCheck.IntervalSec == 0 {
			svc.HealthCheck.IntervalSec = cfg.HealthInterval
		}
		if svc.HealthCheck.TimeoutSec == 0 {
			svc.HealthCheck.TimeoutSec = 2
		}
		if svc.HealthCheck.ExpectedStatus == 0 {
			svc.HealthCheck.ExpectedStatus = 200
		}

		// Service-level circuit breaker defaults
		if svc.CircuitBreaker == nil {
			svc.CircuitBreaker = &CircuitBreakerConfig{}
		}
		if svc.CircuitBreaker.Threshold == 0 {
			svc.CircuitBreaker.Threshold = cfg.BreakerThreshold
		}
		if svc.CircuitBreaker.RecoveryTimeoutSec == 0 {
			svc.CircuitBreaker.RecoveryTimeoutSec = cfg.BreakerTimeoutSec
		}

		// Service-level load balancer defaults
		if svc.LoadBalancer == nil {
			svc.LoadBalancer = &LoadBalancerConfig{}
		}
		if svc.LoadBalancer.Algorithm == "" {
			svc.LoadBalancer.Algorithm = cfg.Algorithm
		}

		// Per-server defaults within the service (inherit from service-level health check)
		for i := range svc.Servers {
			s := &svc.Servers[i]
			if s.Weight == 0 {
				s.Weight = 1
			}
			if s.HealthCheck.Path == "" {
				s.HealthCheck.Path = svc.HealthCheck.Path
			}
			if s.HealthCheck.IntervalSec == 0 {
				s.HealthCheck.IntervalSec = svc.HealthCheck.IntervalSec
			}
			if s.HealthCheck.TimeoutSec == 0 {
				s.HealthCheck.TimeoutSec = svc.HealthCheck.TimeoutSec
			}
			if s.HealthCheck.ExpectedStatus == 0 {
				s.HealthCheck.ExpectedStatus = svc.HealthCheck.ExpectedStatus
			}
		}
	}

	// Backward compatibility: synthesize entrypoints from legacy ports
	if len(cfg.EntryPoints) == 0 {
		cfg.EntryPoints = map[string]*EntryPointConfig{
			"web": {
				Address:  fmt.Sprintf(":%d", cfg.ListenPort),
				Protocol: "http",
			},
			"dashboard": {
				Address:  fmt.Sprintf(":%d", cfg.DashboardPort),
				Protocol: "http",
			},
		}
		if cfg.TLS.Enabled {
			cfg.EntryPoints["web"].Protocol = "https"
			cfg.EntryPoints["web"].TLS = &cfg.TLS
		}
	}

	// Apply entrypoint defaults
	for _, ep := range cfg.EntryPoints {
		if ep.Protocol == "" {
			ep.Protocol = "http"
		}
	}
}

// validate checks the config for semantic errors, like a backend appearing in multiple services.
func validate(cfg *Config) error {
	seenURLs := make(map[string]string)
	for svcName, svc := range cfg.Services {
		for _, srv := range svc.Servers {
			if existingSvc, ok := seenURLs[srv.URL]; ok {
				return fmt.Errorf("backend URL %q is configured in multiple services (%q and %q)", srv.URL, existingSvc, svcName)
			}
			seenURLs[srv.URL] = svcName
		}
	}
	return nil
}

