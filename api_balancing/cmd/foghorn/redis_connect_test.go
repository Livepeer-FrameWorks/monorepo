package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

var errSentinelDown = errors.New("sentinel quorum not reachable")

func TestConnectRequiredRedisRetriesUntilTopologyReady(t *testing.T) {
	calls := 0
	want := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = want.Close() })
	connect := func(context.Context, pkgredis.Config) (goredis.UniversalClient, error) {
		calls++
		if calls < 3 {
			return nil, errSentinelDown
		}
		return want, nil
	}

	got, err := connectRequiredRedis(context.Background(), pkgredis.Config{Mode: pkgredis.ModeSentinel}, connect, 5, time.Millisecond, logging.NewLoggerWithService("test"))
	if err != nil {
		t.Fatalf("connectRequiredRedis: %v", err)
	}
	if got != want || calls != 3 {
		t.Fatalf("client = %v after %d calls, want the third attempt's client", got, calls)
	}
}

func TestConnectRequiredRedisFailsAfterBoundedAttempts(t *testing.T) {
	calls := 0
	connect := func(context.Context, pkgredis.Config) (goredis.UniversalClient, error) {
		calls++
		return nil, errSentinelDown
	}

	_, err := connectRequiredRedis(context.Background(), pkgredis.Config{Mode: pkgredis.ModeSentinel}, connect, 3, time.Millisecond, logging.NewLoggerWithService("test"))
	if !errors.Is(err, errSentinelDown) {
		t.Fatalf("err = %v, want wrapped connection error", err)
	}
	if calls != 3 {
		t.Fatalf("attempts = %d, want exactly 3", calls)
	}
}

func TestConnectRequiredRedisStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	connect := func(context.Context, pkgredis.Config) (goredis.UniversalClient, error) {
		calls++
		cancel()
		return nil, errSentinelDown
	}

	_, err := connectRequiredRedis(ctx, pkgredis.Config{Mode: pkgredis.ModeSentinel}, connect, 10, time.Hour, logging.NewLoggerWithService("test"))
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want context cancellation after the first attempt", err, calls)
	}
}
