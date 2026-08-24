package config

import (
	"reflect"
	"testing"

	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
)

func clearRedisEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"REDIS_URL",
		"REDIS_MODE",
		"REDIS_ADDRS",
		"REDIS_MASTER_NAME",
		"REDIS_USERNAME",
		"REDIS_PASSWORD",
		"REDIS_SENTINEL_USERNAME",
		"REDIS_SENTINEL_PASSWORD",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadSingleNodeRedisURL(t *testing.T) {
	clearRedisEnv(t)
	t.Setenv("REDIS_URL", "redis://foghorn-redis:6379/0")

	cfg := Load()
	if cfg.RedisURL != "redis://foghorn-redis:6379/0" {
		t.Fatalf("RedisURL = %q", cfg.RedisURL)
	}
	if cfg.Redis.Mode != "" || len(cfg.Redis.Addrs) != 0 {
		t.Fatalf("topology config unexpectedly populated: %+v", cfg.Redis)
	}
}

func TestLoadSentinelRedisTopology(t *testing.T) {
	clearRedisEnv(t)
	t.Setenv("REDIS_MODE", "sentinel")
	t.Setenv("REDIS_ADDRS", "sentinel-1:26379,sentinel-2:26379,sentinel-3:26379")
	t.Setenv("REDIS_MASTER_NAME", "foghorn")
	t.Setenv("REDIS_USERNAME", "foghorn-app")
	t.Setenv("REDIS_PASSWORD", "data-secret")
	t.Setenv("REDIS_SENTINEL_USERNAME", "sentinel-app")
	t.Setenv("REDIS_SENTINEL_PASSWORD", "sentinel-secret")

	cfg := Load()
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
