package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

const (
	sentinelConnectAttempts       = 10
	sentinelConnectInitialBackoff = 250 * time.Millisecond
	sentinelConnectMaxBackoff     = 5 * time.Second
)

type redisConnector func(ctx context.Context, cfg pkgredis.Config) (goredis.UniversalClient, error)

// connectRequiredRedis connects to a Redis topology Foghorn cannot run without,
// retrying with capped exponential backoff while the primary, replicas, and
// Sentinels finish starting. It returns the last connection error once every
// attempt has failed.
func connectRequiredRedis(ctx context.Context, cfg pkgredis.Config, connect redisConnector, attempts int, maxBackoff time.Duration, logger logging.Logger) (goredis.UniversalClient, error) {
	if attempts < 1 {
		attempts = 1
	}
	backoff := sentinelConnectInitialBackoff
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		client, err := connect(ctx, cfg)
		if err == nil {
			return client, nil
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		logger.WithError(err).WithFields(logging.Fields{
			"attempt":  attempt,
			"attempts": attempts,
			"retry_in": backoff.String(),
		}).Warn("Redis Sentinel not ready; retrying")
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, maxBackoff)
	}
	return nil, fmt.Errorf("connect to redis after %d attempts: %w", attempts, lastErr)
}
