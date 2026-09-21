package config

import (
	"reflect"
	"testing"

	"frameworks/api_balancing/internal/appconfig"

	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
)

func TestNewSingleNodeRedisURL(t *testing.T) {
	cfg := New(&appconfig.Foghorn{FoghornRedis: appconfig.FoghornRedis{RedisURL: "redis://foghorn-redis:6379/0"}})
	if cfg.RedisURL != "redis://foghorn-redis:6379/0" {
		t.Fatalf("RedisURL = %q", cfg.RedisURL)
	}
	if cfg.Redis.Mode != "" || len(cfg.Redis.Addrs) != 0 {
		t.Fatalf("topology config unexpectedly populated: %+v", cfg.Redis)
	}
	if cfg.ClusterID != "default" {
		t.Fatalf("ClusterID = %q, want default for an unset CLUSTER_ID", cfg.ClusterID)
	}
}

func TestNewSentinelRedisTopology(t *testing.T) {
	cfg := New(&appconfig.Foghorn{
		ClusterID: "media-eu-1",
		FoghornRedis: appconfig.FoghornRedis{
			RedisMode:             "sentinel",
			RedisAddrs:            []string{"sentinel-1:26379", "sentinel-2:26379", "sentinel-3:26379"},
			RedisMasterName:       "foghorn",
			RedisUsername:         "foghorn-app",
			RedisPassword:         "data-secret",
			RedisSentinelUsername: "sentinel-app",
			RedisSentinelPassword: "sentinel-secret",
		},
	})
	if cfg.ClusterID != "media-eu-1" {
		t.Fatalf("ClusterID = %q", cfg.ClusterID)
	}
	if cfg.Redis.Mode != pkgredis.ModeSentinel {
		t.Fatalf("Mode = %q", cfg.Redis.Mode)
	}
	wantAddrs := []string{"sentinel-1:26379", "sentinel-2:26379", "sentinel-3:26379"}
	if !reflect.DeepEqual(cfg.Redis.Addrs, wantAddrs) {
		t.Fatalf("Addrs = %#v, want %#v", cfg.Redis.Addrs, wantAddrs)
	}
	if cfg.Redis.MasterName != "foghorn" {
		t.Fatalf("MasterName = %q", cfg.Redis.MasterName)
	}
	if cfg.Redis.Username != "foghorn-app" || cfg.Redis.Password != "data-secret" {
		t.Fatalf("data credentials not loaded: username=%q password=%q", cfg.Redis.Username, cfg.Redis.Password)
	}
	if cfg.Redis.SentinelUsername != "sentinel-app" || cfg.Redis.SentinelPassword != "sentinel-secret" {
		t.Fatalf("sentinel credentials not loaded: username=%q password=%q", cfg.Redis.SentinelUsername, cfg.Redis.SentinelPassword)
	}
}
