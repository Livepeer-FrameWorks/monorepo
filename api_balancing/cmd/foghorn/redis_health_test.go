package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	goredis "github.com/redis/go-redis/v9"
)

type fakeRedisPinger struct{ err error }

func (f fakeRedisPinger) Ping(ctx context.Context) *goredis.StatusCmd {
	cmd := goredis.NewStatusCmd(ctx, "ping")
	if f.err != nil {
		cmd.SetErr(f.err)
	} else {
		cmd.SetVal("PONG")
	}
	return cmd
}

func TestObserveRedisConnectivityTracksPingResult(t *testing.T) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_redis_connected"})

	observeRedisConnectivity(context.Background(), fakeRedisPinger{}, gauge)
	if got := testutil.ToFloat64(gauge); got != 1 {
		t.Fatalf("gauge after successful ping = %v, want 1", got)
	}
	observeRedisConnectivity(context.Background(), fakeRedisPinger{err: errors.New("connection refused")}, gauge)
	if got := testutil.ToFloat64(gauge); got != 0 {
		t.Fatalf("gauge after failed ping = %v, want 0", got)
	}
}

func TestRedisHealthTracksLossAndRecovery(t *testing.T) {
	client := &fakeRedisPinger{}
	checker := monitoring.NewHealthChecker("foghorn", "test")
	checker.AddCheck("redis", redisHealthCheck(client))
	if got := checker.CheckHealth().Status; got != monitoring.StatusHealthy {
		t.Fatalf("connected Redis health=%s", got)
	}
	client.err = errors.New("connection refused")
	if got := checker.CheckHealth().Status; got != monitoring.StatusUnhealthy {
		t.Fatalf("lost Redis health=%s; required placement dependency must not be healthy", got)
	}
	client.err = nil
	if got := checker.CheckHealth().Status; got != monitoring.StatusHealthy {
		t.Fatalf("recovered Redis health=%s", got)
	}
}
