package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"intelligent-lb/config"
)

// Middleware is a function that wraps an http.Handler to add processing
// before and/or after the next handler in the chain.
// This pattern mirrors Traefik's middleware architecture.
type Middleware func(http.Handler) http.Handler

// Chain composes multiple middlewares into a single Middleware.
// Middlewares execute left-to-right: Chain(A, B, C)(handler)
// produces A(B(C(handler))), so the request flows A → B → C → handler.
func Chain(middlewares ...Middleware) Middleware {
	return func(final http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}

// ServiceRegistry provides access to service-level components.
type ServiceRegistry interface {
	RecordCircuitBreakerResult(url string, success bool)
}

// Builder constructs middleware instances from the config.json middlewares block.
// Each middleware is identified by a unique name and has a type + type-specific config.
// Inspired by Traefik's middleware builder in pkg/server/middleware/.
type Builder struct {
	cfg      *config.Config
	registry ServiceRegistry
	cache    map[string]Middleware
	mu       sync.Mutex
}

// NewBuilder creates a new middleware Builder from the given config.
func NewBuilder(cfg *config.Config, registry ServiceRegistry) *Builder {
	return &Builder{
		cfg:      cfg,
		registry: registry,
		cache:    make(map[string]Middleware),
	}
}

// rateLimitConfig is the deserialized config for the rateLimit middleware type.
type rateLimitConfig struct {
	RequestsPerSecond float64 `json:"requests_per_second"`
	Burst             int     `json:"burst"`
}

// basicAuthConfig is the deserialized config for the basicAuth middleware type.
type basicAuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// retryConfig is the deserialized config for the retry middleware type.
type retryConfig struct {
	Attempts          int `json:"attempts"`
	InitialIntervalMs int `json:"initial_interval_ms"`
}

// accessLogConfig is the deserialized config for the accessLog middleware type.
type accessLogConfig struct {
	FilePath string `json:"file_path"`
}

// timeoutConfig is the deserialized config for the timeout middleware type.
type timeoutMwConfig struct {
	HighSec   int `json:"high_sec"`
	MediumSec int `json:"medium_sec"`
	LowSec    int `json:"low_sec"`
}

// circuitBreakerConfig is the deserialized config for the circuitBreaker middleware type.
type circuitBreakerConfig struct {
	Threshold          int `json:"threshold"`
	RecoveryTimeoutSec int `json:"recovery_timeout_sec"`
}

// corsConfigJSON is the deserialized config for the cors middleware type.
type corsConfigJSON struct {
	AllowedOrigins []string `json:"allowed_origins"`
	AllowedMethods []string `json:"allowed_methods"`
	AllowedHeaders []string `json:"allowed_headers"`
}

// Build constructs a single named middleware from the config.
// If the middleware name matches a legacy name (headers, cors) and no
// middlewares block is configured, it falls back to legacy behavior.
func (b *Builder) Build(name string) (Middleware, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if mw, ok := b.cache[name]; ok {
		return mw, nil
	}

	var mw Middleware
	var err error

	// Check the named middlewares config block first
	if b.cfg.Middlewares != nil {
		if mwCfg, ok := b.cfg.Middlewares[name]; ok {
			mw, err = b.buildFromConfig(name, mwCfg)
		}
	}

	if mw == nil && err == nil {
		// Fallback to legacy resolution for backward compatibility
		mw, err = b.buildLegacy(name)
	}

	if err != nil {
		return nil, err
	}
	b.cache[name] = mw
	return mw, nil
}

// BuildChain builds an ordered chain of middlewares from a list of names.
func (b *Builder) BuildChain(names []string) ([]Middleware, error) {
	var middlewares []Middleware
	for _, name := range names {
		mw, err := b.Build(name)
		if err != nil {
			return nil, fmt.Errorf("building middleware %q: %w", name, err)
		}
		middlewares = append(middlewares, mw)
	}
	return middlewares, nil
}

// buildFromConfig instantiates a middleware from a typed config entry.
func (b *Builder) buildFromConfig(name string, mwCfg *config.MiddlewareConfig) (Middleware, error) {
	switch mwCfg.Type {
	case "rateLimit":
		var cfg rateLimitConfig
		if err := json.Unmarshal(mwCfg.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing rateLimit config for %q: %w", name, err)
		}
		if cfg.RequestsPerSecond == 0 {
			cfg.RequestsPerSecond = b.cfg.RateLimitRPS
		}
		if cfg.Burst == 0 {
			cfg.Burst = b.cfg.RateLimitBurst
		}
		rl := NewPerIPRateLimiter(cfg.RequestsPerSecond, cfg.Burst)
		return rl.Middleware(), nil

	case "basicAuth":
		var cfg basicAuthConfig
		if err := json.Unmarshal(mwCfg.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing basicAuth config for %q: %w", name, err)
		}
		return BasicAuth(cfg.Username, cfg.Password), nil

	case "retry":
		var cfg retryConfig
		if err := json.Unmarshal(mwCfg.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing retry config for %q: %w", name, err)
		}
		if cfg.Attempts == 0 {
			cfg.Attempts = b.cfg.MaxRetries
		}
		if cfg.InitialIntervalMs == 0 {
			cfg.InitialIntervalMs = b.cfg.RetryBackoffMs
		}
		return NewRetry(cfg.Attempts, cfg.InitialIntervalMs), nil

	case "accessLog":
		var cfg accessLogConfig
		if err := json.Unmarshal(mwCfg.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing accessLog config for %q: %w", name, err)
		}
		if cfg.FilePath == "" {
			cfg.FilePath = b.cfg.AccessLogPath
		}
		return NewAccessLog(cfg.FilePath), nil

	case "headers":
		return RequestHeaders(), nil

	case "timeout":
		var cfg timeoutMwConfig
		if err := json.Unmarshal(mwCfg.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing timeout config for %q: %w", name, err)
		}
		if cfg.HighSec == 0 {
			cfg.HighSec = b.cfg.Timeouts.HighSec
		}
		if cfg.MediumSec == 0 {
			cfg.MediumSec = b.cfg.Timeouts.MediumSec
		}
		if cfg.LowSec == 0 {
			cfg.LowSec = b.cfg.Timeouts.LowSec
		}
		return NewPriorityTimeout(cfg.HighSec, cfg.MediumSec, cfg.LowSec), nil

	case "circuitBreaker":
		var cfg circuitBreakerConfig
		if err := json.Unmarshal(mwCfg.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing circuitBreaker config for %q: %w", name, err)
		}
		if cfg.Threshold == 0 {
			cfg.Threshold = b.cfg.BreakerThreshold
		}
		if cfg.RecoveryTimeoutSec == 0 {
			cfg.RecoveryTimeoutSec = b.cfg.BreakerTimeoutSec
		}
		return NewCircuitBreaker(b.registry), nil

	case "cors":
		var cfg corsConfigJSON
		if err := json.Unmarshal(mwCfg.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing cors config for %q: %w", name, err)
		}
		corsConfig := DefaultCORSConfig()
		if len(cfg.AllowedOrigins) > 0 {
			corsConfig.AllowedOrigins = cfg.AllowedOrigins
		}
		if len(cfg.AllowedMethods) > 0 {
			corsConfig.AllowedMethods = cfg.AllowedMethods
		}
		if len(cfg.AllowedHeaders) > 0 {
			corsConfig.AllowedHeaders = cfg.AllowedHeaders
		}
		return CORS(corsConfig), nil

	default:
		return nil, fmt.Errorf("unknown middleware type %q for %q", mwCfg.Type, name)
	}
}

// buildLegacy creates a middleware using legacy config fields (backward compatibility).
func (b *Builder) buildLegacy(name string) (Middleware, error) {
	switch name {
	case "rate-limit":
		rl := NewPerIPRateLimiter(b.cfg.RateLimitRPS, b.cfg.RateLimitBurst)
		return rl.Middleware(), nil
	case "headers":
		return RequestHeaders(), nil
	case "cors":
		corsConfig := DefaultCORSConfig()
		if len(b.cfg.CORS.AllowedOrigins) > 0 {
			corsConfig.AllowedOrigins = b.cfg.CORS.AllowedOrigins
		}
		if len(b.cfg.CORS.AllowedMethods) > 0 {
			corsConfig.AllowedMethods = b.cfg.CORS.AllowedMethods
		}
		if len(b.cfg.CORS.AllowedHeaders) > 0 {
			corsConfig.AllowedHeaders = b.cfg.CORS.AllowedHeaders
		}
		return CORS(corsConfig), nil
	case "basic-auth":
		return BasicAuth(b.cfg.DashboardAuth.Username, b.cfg.DashboardAuth.Password), nil
	case "access-log":
		return NewAccessLog(b.cfg.AccessLogPath), nil
	case "retry":
		return NewRetry(b.cfg.MaxRetries, b.cfg.RetryBackoffMs), nil
	case "timeout":
		return NewPriorityTimeout(
			b.cfg.Timeouts.HighSec,
			b.cfg.Timeouts.MediumSec,
			b.cfg.Timeouts.LowSec,
		), nil
	default:
		return nil, fmt.Errorf("unknown legacy middleware %q", name)
	}
}
