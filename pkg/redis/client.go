package redis

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	Mode             Mode
	Addrs            []string // single: 1 addr, sentinel: sentinel addrs, cluster: seed nodes
	MasterName       string   // sentinel only
	Username         string
	Password         string
	SentinelUsername string // sentinel only
	SentinelPassword string // sentinel only
	DB               int
	DialTimeout      time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
}

// NewUniversalClient creates a Redis client for the explicitly selected
// single-node, Sentinel, or Cluster topology.
func NewUniversalClient(ctx context.Context, cfg Config) (goredis.UniversalClient, error) {
	return newUniversalClient(ctx, cfg, nil)
}

func newUniversalClient(ctx context.Context, cfg Config, dialer func(context.Context, string, string) (net.Conn, error)) (goredis.UniversalClient, error) {
	if len(cfg.Addrs) == 0 {
		return nil, fmt.Errorf("at least one redis address is required")
	}
	if cfg.Mode == "" {
		return nil, errors.New("redis mode is required")
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

	var client goredis.UniversalClient
	switch cfg.Mode {
	case ModeSingle:
		if len(cfg.Addrs) != 1 {
			return nil, fmt.Errorf("single redis mode requires exactly one address, got %d", len(cfg.Addrs))
		}
		client = goredis.NewClient(&goredis.Options{
			Addr: cfg.Addrs[0], Username: cfg.Username, Password: cfg.Password, DB: cfg.DB,
			Dialer: dialer, DialTimeout: dialTimeout, ReadTimeout: readTimeout, WriteTimeout: writeTimeout,
		})
	case ModeSentinel:
		if cfg.MasterName == "" {
			return nil, errors.New("sentinel redis mode requires master name")
		}
		client = goredis.NewFailoverClient(&goredis.FailoverOptions{
			MasterName: cfg.MasterName, SentinelAddrs: cfg.Addrs,
			Username: cfg.Username, Password: cfg.Password,
			SentinelUsername: cfg.SentinelUsername, SentinelPassword: cfg.SentinelPassword,
			DB: cfg.DB, Dialer: dialer, DialTimeout: dialTimeout, ReadTimeout: readTimeout, WriteTimeout: writeTimeout,
		})
	case ModeCluster:
		if cfg.DB != 0 {
			return nil, errors.New("cluster redis mode supports only database 0")
		}
		clusterClient := goredis.NewClusterClient(&goredis.ClusterOptions{
			Addrs: cfg.Addrs, Username: cfg.Username, Password: cfg.Password,
			Dialer: dialer, DialTimeout: dialTimeout, ReadTimeout: readTimeout, WriteTimeout: writeTimeout,
		})
		if _, err := clusterClient.ClusterShards(ctx).Result(); err != nil {
			_ = clusterClient.Close()
			return nil, fmt.Errorf("load redis cluster topology: %w", err)
		}
		client = clusterClient
	default:
		return nil, fmt.Errorf("unsupported redis mode %q", cfg.Mode)
	}
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}

// NewClientFromURL creates a single-node Redis client from the canonical URL
// configuration used by local and single-node deployments.
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
