package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestTenantViewerCapacityAdmissionDeadline(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "redis"}[shared], func(t *testing.T) {
			m := NewTenantCapacityManager()
			if shared {
				server := miniredis.RunT(t)
				server.SetTime(time.Now())
				client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
				t.Cleanup(func() { _ = client.Close() })
				m.EnableRedisSync(client, "cell")
			}
			for _, until := range []time.Time{{}, time.Now().Add(-time.Second)} {
				allowed, added, _, err := m.TryRegisterViewerBefore(context.Background(), "tenant", "node", "session", "viewer", 1, until)
				if allowed || added || !errors.Is(err, ErrViewerAdmissionExpired) || m.CountViewers("tenant") != 0 {
					t.Fatalf("expired placement mutated capacity: %v %v %v", allowed, added, err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if allowed, _, _, err := m.TryRegisterViewerBefore(ctx, "tenant", "node", "session", "viewer", 1, time.Now().Add(time.Minute)); allowed || !errors.Is(err, context.Canceled) || m.CountViewers("tenant") != 0 {
				t.Fatalf("canceled placement mutated capacity: %v", err)
			}
			for attempt := range 2 {
				allowed, added, count, err := m.TryRegisterViewerBefore(context.Background(), "tenant", "node", "session", "viewer", 1, time.Now().Add(time.Minute))
				if !allowed || added != (attempt == 0) || count != 1 || err != nil {
					t.Fatalf("valid admission/retry failed: %v %v %d %v", allowed, added, count, err)
				}
			}
			if allowed, _, count, err := m.TryRegisterViewerBefore(context.Background(), "tenant", "node", "other", "other", 1, time.Now().Add(time.Minute)); allowed || count != 1 || err != nil {
				t.Fatalf("placement bypassed viewer cap: %v %d %v", allowed, count, err)
			}
		})
	}
}

func TestTenantViewerCapacityRedisRejectsDeadlineAtExecution(t *testing.T) {
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	m := NewTenantCapacityManager()
	m.EnableRedisSync(client, "cell")
	if allowed, _, _, err := m.TryRegisterViewer("tenant", "node", "existing", "viewer", 1); !allowed || err != nil {
		t.Fatal(err)
	}
	before := server.Dump()
	// Application-side time still permits the request; the coordination engine
	// observes expiry before its script can renew or create any session state.
	server.SetTime(time.Now().Add(time.Minute))
	for _, session := range []string{"existing", "new"} {
		allowed, added, _, err := m.TryRegisterViewerBefore(context.Background(), "tenant", "node", session, "viewer", 1, time.Now().Add(10*time.Second))
		if allowed || added || !errors.Is(err, ErrViewerAdmissionExpired) {
			t.Fatalf("late Redis execution admitted viewer: %v %v %v", allowed, added, err)
		}
		if after := server.Dump(); after != before {
			t.Fatal("expired execution mutated or renewed coordination state")
		}
	}
}
