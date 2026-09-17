package main

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"
)

const (
	redisConnectivityInterval = 15 * time.Second
	redisConnectivityTimeout  = 2 * time.Second
)

type redisPinger interface {
	Ping(ctx context.Context) *goredis.StatusCmd
}

// runRedisConnectivityGauge reports whether this Foghorn can currently reach
// its cell's Redis. Startup waits for a Sentinel topology, but a later loss
// does not stop the process, so this gauge is what the
// FoghornRedisDisconnected alert reads.
func runRedisConnectivityGauge(ctx context.Context, client redisPinger, gauge prometheus.Gauge) {
	ticker := time.NewTicker(redisConnectivityInterval)
	defer ticker.Stop()
	for {
		observeRedisConnectivity(ctx, client, gauge)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func observeRedisConnectivity(ctx context.Context, client redisPinger, gauge prometheus.Gauge) {
	pingCtx, cancel := context.WithTimeout(ctx, redisConnectivityTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		gauge.Set(0)
		return
	}
	gauge.Set(1)
}
