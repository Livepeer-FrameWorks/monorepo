package clients

import (
	"fmt"
	"time"

	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/deckhand"
	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/clients/periscope"
	"frameworks/pkg/clients/purser"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/clients/signalman"
	skipperclient "frameworks/pkg/clients/skipper"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
)

// ServiceClients holds all downstream service gRPC clients
type ServiceClients struct {
	Commodore     *commodore.GRPCClient
	Deckhand      *deckhand.GRPCClient
	Decklog       *decklog.BatchedClient
	Periscope     *periscope.GRPCClient
	Purser        *purser.GRPCClient
	Quartermaster *quartermaster.GRPCClient
	Signalman     *signalman.GRPCClient
	Skipper       *skipperclient.GRPCClient
}

// Config represents the configuration for all service clients
type Config struct {
	ServiceToken string
	Timeout      time.Duration
	Logger       logging.Logger
}

// NewServiceClients creates and initializes all downstream service gRPC clients
func NewServiceClients(cfg Config) (*ServiceClients, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	// Quartermaster cache
	qmTTL := time.Duration(config.GetEnvInt("QUARTERMASTER_CACHE_TTL_SECONDS", 60)) * time.Second
	qmSWR := time.Duration(config.GetEnvInt("QUARTERMASTER_CACHE_SWR_SECONDS", 30)) * time.Second
	qmNeg := time.Duration(config.GetEnvInt("QUARTERMASTER_CACHE_NEG_TTL_SECONDS", 10)) * time.Second
	qmMax := config.GetEnvInt("QUARTERMASTER_CACHE_MAX", 10000)
	qmCache := cache.New(cache.Options{TTL: qmTTL, StaleWhileRevalidate: qmSWR, NegativeTTL: qmNeg, MaxEntries: qmMax}, cache.MetricsHooks{})

	// Initialize Commodore gRPC client
	commodoreClient, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:     config.RequireEnv("COMMODORE_GRPC_ADDR"),
		Timeout:      cfg.Timeout,
		Logger:       cfg.Logger,
		Cache:        qmCache, // Used for stream key validation caching
		ServiceToken: cfg.ServiceToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Commodore gRPC client: %w", err)
	}

	// Initialize Periscope gRPC client
	periscopeClient, err := periscope.NewGRPCClient(periscope.GRPCConfig{
		GRPCAddr:     config.RequireEnv("PERISCOPE_GRPC_ADDR"),
		Timeout:      cfg.Timeout,
		Logger:       cfg.Logger,
		ServiceToken: cfg.ServiceToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Periscope gRPC client: %w", err)
	}

	// Initialize Purser gRPC client
	purserClient, err := purser.NewGRPCClient(purser.GRPCConfig{
		GRPCAddr:     config.RequireEnv("PURSER_GRPC_ADDR"),
		Timeout:      cfg.Timeout,
		Logger:       cfg.Logger,
		ServiceToken: cfg.ServiceToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Purser gRPC client: %w", err)
	}

	// Initialize Quartermaster gRPC client
	quartermasterClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:     config.RequireEnv("QUARTERMASTER_GRPC_ADDR"),
		Timeout:      cfg.Timeout,
		Logger:       cfg.Logger,
		ServiceToken: cfg.ServiceToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Quartermaster gRPC client: %w", err)
	}

	// Initialize Signalman gRPC client
	signalmanClient, err := signalman.NewGRPCClient(signalman.GRPCConfig{
		GRPCAddr:     config.RequireEnv("SIGNALMAN_GRPC_ADDR"),
		Timeout:      cfg.Timeout,
		Logger:       cfg.Logger,
		ServiceToken: cfg.ServiceToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Signalman gRPC client: %w", err)
	}

	// Initialize Decklog gRPC client (for API usage tracking)
	decklogClient, err := decklog.NewBatchedClient(decklog.BatchedClientConfig{
		Target:        config.RequireEnv("DECKLOG_GRPC_ADDR"),
		AllowInsecure: true, // Internal service communication
		Timeout:       cfg.Timeout,
		Source:        "bridge",
		ServiceToken:  cfg.ServiceToken,
	}, cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create Decklog gRPC client: %w", err)
	}

	// Initialize Deckhand gRPC client (for support messaging)
	// Optional: only initialize if DECKHAND_GRPC_ADDR is configured
	var deckhandClient *deckhand.GRPCClient
	if deckhandAddr := config.GetEnv("DECKHAND_GRPC_ADDR", ""); deckhandAddr != "" {
		deckhandClient, err = deckhand.NewGRPCClient(deckhand.GRPCConfig{
			GRPCAddr:     deckhandAddr,
			Timeout:      cfg.Timeout,
			Logger:       cfg.Logger,
			ServiceToken: cfg.ServiceToken,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Deckhand gRPC client: %w", err)
		}
	}

	// Initialize Skipper gRPC client (for AI consultant)
	// Optional: only initialize if SKIPPER_GRPC_ADDR is configured
	var skipperClient *skipperclient.GRPCClient
	if skipperAddr := config.GetEnv("SKIPPER_GRPC_ADDR", ""); skipperAddr != "" {
		skipperClient, err = skipperclient.NewGRPCClient(skipperclient.GRPCConfig{
			GRPCAddr:     skipperAddr,
			Timeout:      cfg.Timeout,
			Logger:       cfg.Logger,
			ServiceToken: cfg.ServiceToken,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Skipper gRPC client: %w", err)
		}
	}

	return &ServiceClients{
		Commodore:     commodoreClient,
		Deckhand:      deckhandClient,
		Decklog:       decklogClient,
		Periscope:     periscopeClient,
		Purser:        purserClient,
		Quartermaster: quartermasterClient,
		Signalman:     signalmanClient,
		Skipper:       skipperClient,
	}, nil
}

// Close closes all gRPC connections
func (c *ServiceClients) Close() error {
	var errs []error

	if c.Commodore != nil {
		if err := c.Commodore.Close(); err != nil {
			errs = append(errs, fmt.Errorf("commodore: %w", err))
		}
	}
	if c.Deckhand != nil {
		if err := c.Deckhand.Close(); err != nil {
			errs = append(errs, fmt.Errorf("deckhand: %w", err))
		}
	}
	if c.Decklog != nil {
		if err := c.Decklog.Close(); err != nil {
			errs = append(errs, fmt.Errorf("decklog: %w", err))
		}
	}
	if c.Periscope != nil {
		if err := c.Periscope.Close(); err != nil {
			errs = append(errs, fmt.Errorf("periscope: %w", err))
		}
	}
	if c.Purser != nil {
		if err := c.Purser.Close(); err != nil {
			errs = append(errs, fmt.Errorf("purser: %w", err))
		}
	}
	if c.Quartermaster != nil {
		if err := c.Quartermaster.Close(); err != nil {
			errs = append(errs, fmt.Errorf("quartermaster: %w", err))
		}
	}
	if c.Signalman != nil {
		if err := c.Signalman.Close(); err != nil {
			errs = append(errs, fmt.Errorf("signalman: %w", err))
		}
	}
	if c.Skipper != nil {
		if err := c.Skipper.Close(); err != nil {
			errs = append(errs, fmt.Errorf("skipper: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing clients: %v", errs)
	}
	return nil
}
