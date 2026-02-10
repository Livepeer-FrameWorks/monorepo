package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const defaultDialTimeout = 5 * time.Second

func NewClientFromURL(ctx context.Context, redisURL string) (*goredis.Client, error) {
	if redisURL == "" {
		return nil, fmt.Errorf("redis url is required")
	}

	opts, err := goredis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	if opts.DialTimeout == 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = defaultDialTimeout
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = defaultDialTimeout
	}

	client := goredis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}
