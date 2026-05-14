package config

import (
	"strings"

	pkgconfig "github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
)

type Config struct {
	ClusterID string
	RedisURL  string
	Redis     pkgredis.Config
}

func Load() Config {
	addrs := pkgconfig.GetEnv("REDIS_ADDRS", "")
	var addrList []string
	if addrs != "" {
		addrList = strings.Split(addrs, ",")
	}

	return Config{
		ClusterID: pkgconfig.GetEnv("CLUSTER_ID", "default"),
		RedisURL:  pkgconfig.GetEnv("REDIS_URL", ""),
		Redis: pkgredis.Config{
			Mode:             pkgredis.Mode(pkgconfig.GetEnv("REDIS_MODE", "")),
			Addrs:            addrList,
			MasterName:       pkgconfig.GetEnv("REDIS_MASTER_NAME", ""),
			Username:         pkgconfig.GetEnv("REDIS_USERNAME", ""),
			Password:         pkgconfig.GetEnv("REDIS_PASSWORD", ""),
			SentinelUsername: pkgconfig.GetEnv("REDIS_SENTINEL_USERNAME", ""),
			SentinelPassword: pkgconfig.GetEnv("REDIS_SENTINEL_PASSWORD", ""),
		},
	}
}
