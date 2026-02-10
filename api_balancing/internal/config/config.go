package config

import (
	pkgconfig "frameworks/pkg/config"
)

type Config struct {
	ClusterID string
	RedisURL  string
}

func Load() Config {
	return Config{
		ClusterID: pkgconfig.GetEnv("CLUSTER_ID", "default"),
		RedisURL:  pkgconfig.GetEnv("REDIS_URL", ""),
	}
}
