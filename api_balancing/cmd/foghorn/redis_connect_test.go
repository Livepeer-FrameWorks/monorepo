package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"

	"github.com/alicebob/miniredis/v2"
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

func TestConfiguredRedisWaitsForEveryTopology(t *testing.T) {
	for _, mode := range []pkgredis.Mode{pkgredis.ModeSingle, pkgredis.ModeCluster, pkgredis.ModeSentinel, ""} {
		t.Run(string(mode), func(t *testing.T) {
			want := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
			t.Cleanup(func() { _ = want.Close() })
			calls := 0
			connect := func(context.Context, pkgredis.Config) (goredis.UniversalClient, error) {
				calls++
				if calls == 1 {
					return nil, errSentinelDown
				}
				return want, nil
			}
			url := "rediss://user:password@example.test:6380/3?protocol=2"
			connectURL := func(ctx context.Context, gotURL string) (*goredis.Client, error) {
				if gotURL != url {
					t.Fatalf("URL options changed: got %q", gotURL)
				}
				_, err := connect(ctx, pkgredis.Config{})
				if err != nil {
					return nil, err
				}
				return want, nil
			}
			got, err := connectConfiguredRedis(context.Background(), pkgredis.Config{Mode: mode}, url, connect, connectURL, 2, time.Millisecond, logging.NewLoggerWithService("test"))
			if err != nil || got != want || calls != 2 {
				t.Fatalf("configured Redis startup lost its dependency: client=%v err=%v calls=%d; want connected client after 2 attempts", got, err, calls)
			}
		})
	}
}

func TestConfiguredRedisFailureCannotBecomeSuccessfulStartup(t *testing.T) {
	for _, mode := range []pkgredis.Mode{pkgredis.ModeSingle, pkgredis.ModeCluster, pkgredis.ModeSentinel, ""} {
		t.Run(string(mode), func(t *testing.T) {
			calls := 0
			connect := func(context.Context, pkgredis.Config) (goredis.UniversalClient, error) {
				calls++
				return nil, errSentinelDown
			}
			connectURL := func(ctx context.Context, _ string) (*goredis.Client, error) {
				_, err := connect(ctx, pkgredis.Config{})
				return nil, err
			}
			got, err := connectConfiguredRedis(context.Background(), pkgredis.Config{Mode: mode}, "redis://example.test", connect, connectURL, 2, time.Millisecond, logging.NewLoggerWithService("test"))
			if got != nil || !errors.Is(err, errSentinelDown) || calls != 2 {
				t.Fatalf("unreachable configured Redis must prevent startup: client=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestConfiguredRedisURLConnectsAfterServerStarts(t *testing.T) {
	server := miniredis.RunT(t)
	addr := server.Addr()
	server.Close()
	calls := 0
	connectURL := func(ctx context.Context, url string) (*goredis.Client, error) {
		calls++
		client, err := pkgredis.NewClientFromURL(ctx, url)
		if calls == 1 {
			if err == nil {
				t.Fatal("closed Redis unexpectedly connected")
			}
			if startErr := server.StartAddr(addr); startErr != nil {
				t.Fatal(startErr)
			}
		}
		return client, err
	}
	client, err := connectConfiguredRedis(context.Background(), pkgredis.Config{}, "redis://"+addr+"/3?protocol=2&max_retries=-1&dial_timeout=50ms", pkgredis.NewUniversalClient, connectURL, 2, time.Millisecond, logging.NewLoggerWithService("test"))
	if err != nil || client == nil {
		t.Fatalf("server recovered but startup discarded Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if calls != 2 {
		t.Fatalf("attempts=%d, want 2", calls)
	}
	if err := client.Set(context.Background(), "placement-ready", "yes", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if got, err := server.DB(3).Get("placement-ready"); err != nil || got != "yes" {
		t.Fatalf("URL database option lost: got=%q err=%v", got, err)
	}
}
