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
	redisConnectAttempts       = 10
	redisConnectInitialBackoff = 250 * time.Millisecond
	redisConnectMaxBackoff     = 5 * time.Second
)

type redisConnector func(ctx context.Context, cfg pkgredis.Config) (goredis.UniversalClient, error)

// connectRequiredRedis connects to a Redis topology Foghorn cannot run without,
// retrying with capped exponential backoff while the configured service finishes
// starting. It returns the last connection error once every
// attempt has failed.
func connectRequiredRedis(ctx context.Context, cfg pkgredis.Config, connect redisConnector, attempts int, maxBackoff time.Duration, logger logging.Logger) (goredis.UniversalClient, error) {
	if attempts < 1 {
		attempts = 1
	}
	backoff := redisConnectInitialBackoff
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
		}).Warn("Configured Redis not ready; retrying")
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

// connectConfiguredRedis requires every configured topology to connect before
// wiring shared state and placement. A failed initial ping cannot turn a Redis
// cell into an in-memory instance for the process lifetime. URL TLS,
// authentication, database and client options are passed to the URL constructor.
func connectConfiguredRedis(ctx context.Context, cfg pkgredis.Config, redisURL string, connect redisConnector, connectURL func(context.Context, string) (*goredis.Client, error), attempts int, maxBackoff time.Duration, logger logging.Logger) (goredis.UniversalClient, error) {
	if cfg.Mode != "" {
		return connectRequiredRedis(ctx, cfg, connect, attempts, maxBackoff, logger)
	}
	if redisURL != "" {
		return connectRequiredRedis(ctx, cfg, func(ctx context.Context, _ pkgredis.Config) (goredis.UniversalClient, error) {
			client, err := connectURL(ctx, redisURL)
			if err != nil {
				return nil, err
			}
			return client, nil
		}, attempts, maxBackoff, logger)
	}
	return nil, nil
}
