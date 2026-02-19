package qmbootstrap

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// BootstrapClient is the minimum Quartermaster client surface required for service bootstrap.
type BootstrapClient interface {
	BootstrapService(ctx context.Context, req *pb.BootstrapServiceRequest) (*pb.BootstrapServiceResponse, error)
}

// RetryConfig controls retry behavior for service bootstrap registration.
type RetryConfig struct {
	ServiceName    string
	AttemptTimeout time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxAttempts    int
}

// DefaultRetryConfig returns conservative retry defaults for service bootstrap.
func DefaultRetryConfig(serviceName string) RetryConfig {
	return RetryConfig{
		ServiceName:    serviceName,
		AttemptTimeout: 5 * time.Second,
		InitialBackoff: 2 * time.Second,
		MaxBackoff:     30 * time.Second,
		MaxAttempts:    0, // retry forever
	}
}

// BootstrapServiceWithRetry keeps attempting Quartermaster bootstrap until success,
// attempts are exhausted, or the parent context is cancelled.
func BootstrapServiceWithRetry(ctx context.Context, client BootstrapClient, req *pb.BootstrapServiceRequest, logger logging.Logger, cfg RetryConfig) (*pb.BootstrapServiceResponse, error) {
	if client == nil {
		return nil, fmt.Errorf("quartermaster bootstrap client is nil")
	}
	if req == nil {
		return nil, fmt.Errorf("quartermaster bootstrap request is nil")
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = req.GetType()
		if cfg.ServiceName == "" {
			cfg.ServiceName = "unknown"
		}
	}
	if cfg.AttemptTimeout <= 0 {
		cfg.AttemptTimeout = 5 * time.Second
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 2 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}

	backoff := cfg.InitialBackoff
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, cfg.AttemptTimeout)
		resp, err := client.BootstrapService(attemptCtx, req)
		cancel()
		if err == nil {
			return resp, nil
		}

		if cfg.MaxAttempts > 0 && attempt >= cfg.MaxAttempts {
			return nil, err
		}

		logger.WithFields(logging.Fields{
			"service_name": cfg.ServiceName,
			"attempt":      attempt,
			"retry_in":     backoff.String(),
		}).WithError(err).Warn("Quartermaster bootstrap failed; retrying")

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < cfg.MaxBackoff {
			backoff *= 2
			if backoff > cfg.MaxBackoff {
				backoff = cfg.MaxBackoff
			}
		}
	}
}
