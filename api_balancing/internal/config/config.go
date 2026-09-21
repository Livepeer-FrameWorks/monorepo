package config

import (
	"frameworks/api_balancing/internal/appconfig"

	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
)

type Config struct {
	ClusterID string
	RedisURL  string
	Redis     pkgredis.Config
}

// New derives the cluster identity and Redis connection settings from the
// typed Foghorn configuration.
func New(cfg *appconfig.Foghorn) Config {
	return Config{
		ClusterID: cfg.LocalClusterID(),
		RedisURL:  cfg.RedisURL,
		Redis: pkgredis.Config{
			Mode:             pkgredis.Mode(cfg.RedisMode),
			Addrs:            cfg.RedisAddrs,
			MasterName:       cfg.RedisMasterName,
			Username:         cfg.RedisUsername,
			Password:         cfg.RedisPassword,
			SentinelUsername: cfg.RedisSentinelUsername,
			SentinelPassword: cfg.RedisSentinelPassword,
		},
	}
}
