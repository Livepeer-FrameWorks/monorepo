package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const defaultDialTimeout = 5 * time.Second

// Mode selects the Redis deployment topology.
type Mode string

const (
	ModeSingle   Mode = "single"
	ModeSentinel Mode = "sentinel"
	ModeCluster  Mode = "cluster"
)

// Config configures a topology-agnostic Redis connection.
type Config struct {
	Mode         Mode
	Addrs        []string // single: 1 addr, sentinel: sentinel addrs, cluster: seed nodes
	MasterName   string   // sentinel only
	Username     string
	Password     string
	DB           int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// NewUniversalClient creates a Redis client that works with single-node,
// Sentinel, or Cluster topologies based on Config.Mode. go-redis routes
// internally: MasterName set → Sentinel, multiple Addrs → Cluster,
// single Addr → standalone.
func NewUniversalClient(ctx context.Context, cfg Config) (goredis.UniversalClient, error) {
	if len(cfg.Addrs) == 0 {
		return nil, fmt.Errorf("at least one redis address is required")
	}

	dialTimeout := cfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = defaultDialTimeout
	}
	readTimeout := cfg.ReadTimeout
	if readTimeout == 0 {
		readTimeout = defaultDialTimeout
	}
	writeTimeout := cfg.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = defaultDialTimeout
	}

	opts := &goredis.UniversalOptions{
		Addrs:        cfg.Addrs,
		MasterName:   cfg.MasterName,
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  dialTimeout,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}

	client := goredis.NewUniversalClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}

// NewClientFromURL creates a single-node Redis client from a URL.
// Retained for backwards compatibility; prefer NewUniversalClient for new code.
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
