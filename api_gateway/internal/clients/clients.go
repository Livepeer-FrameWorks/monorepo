package clients

import (
	"time"

	"frameworks/pkg/cache"
	"frameworks/pkg/clients"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/periscope"
	"frameworks/pkg/clients/purser"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/clients/signalman"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
)

// ServiceClients holds all downstream service clients
type ServiceClients struct {
	Commodore     *commodore.Client
	Periscope     *periscope.Client
	Purser        *purser.Client
	Quartermaster *quartermaster.Client
	Signalman     *signalman.Client
}

// Config represents the configuration for all service clients
type Config struct {
	ServiceToken         string
	Timeout              time.Duration
	Logger               logging.Logger
	RetryConfig          *clients.RetryConfig
	CircuitBreakerConfig *clients.CircuitBreakerConfig
}

// NewServiceClients creates and initializes all downstream service clients
func NewServiceClients(cfg Config) *ServiceClients {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	// Default retry configuration
	retryConfig := clients.DefaultRetryConfig()
	if cfg.RetryConfig != nil {
		retryConfig = *cfg.RetryConfig
	}

	// Default circuit breaker configuration
	circuitBreakerConfig := &clients.CircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 3,
		Timeout:          60 * time.Second,
	}
	if cfg.CircuitBreakerConfig != nil {
		circuitBreakerConfig = cfg.CircuitBreakerConfig
	}

	// Quartermaster cache
	qmTTL := time.Duration(config.GetEnvInt("QUARTERMASTER_CACHE_TTL_SECONDS", 60)) * time.Second
	qmSWR := time.Duration(config.GetEnvInt("QUARTERMASTER_CACHE_SWR_SECONDS", 30)) * time.Second
	qmNeg := time.Duration(config.GetEnvInt("QUARTERMASTER_CACHE_NEG_TTL_SECONDS", 10)) * time.Second
	qmMax := config.GetEnvInt("QUARTERMASTER_CACHE_MAX", 10000)
	qmCache := cache.New(cache.Options{TTL: qmTTL, StaleWhileRevalidate: qmSWR, NegativeTTL: qmNeg, MaxEntries: qmMax}, cache.MetricsHooks{})

	return &ServiceClients{
		Commodore: commodore.NewClient(commodore.Config{
			BaseURL:              config.GetEnv("COMMODORE_URL", "http://localhost:18001"),
			ServiceToken:         cfg.ServiceToken,
			Timeout:              cfg.Timeout,
			Logger:               cfg.Logger,
			RetryConfig:          &retryConfig,
			CircuitBreakerConfig: circuitBreakerConfig,
		}),
		Periscope: periscope.NewClient(periscope.Config{
			BaseURL:              config.GetEnv("PERISCOPE_QUERY_URL", "http://localhost:18004"),
			ServiceToken:         cfg.ServiceToken,
			Timeout:              cfg.Timeout,
			Logger:               cfg.Logger,
			RetryConfig:          &retryConfig,
			CircuitBreakerConfig: circuitBreakerConfig,
		}),
		Purser: purser.NewClient(purser.Config{
			BaseURL:              config.GetEnv("PURSER_URL", "http://localhost:18003"),
			ServiceToken:         cfg.ServiceToken,
			Timeout:              cfg.Timeout,
			Logger:               cfg.Logger,
			RetryConfig:          &retryConfig,
			CircuitBreakerConfig: circuitBreakerConfig,
		}),
		Quartermaster: quartermaster.NewClient(quartermaster.Config{
			BaseURL:              config.GetEnv("QUARTERMASTER_URL", "http://localhost:18002"),
			ServiceToken:         cfg.ServiceToken,
			Timeout:              cfg.Timeout,
			Logger:               cfg.Logger,
			RetryConfig:          &retryConfig,
			CircuitBreakerConfig: circuitBreakerConfig,
			Cache:                qmCache,
		}),
		Signalman: signalman.NewClient(signalman.Config{
			BaseURL:        config.GetEnv("SIGNALMAN_WS_URL", "ws://localhost:18009"),
			Logger:         cfg.Logger,
			ReconnectDelay: 5 * time.Second,
			MaxReconnects:  5,
		}),
	}
}
