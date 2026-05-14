package resolvers

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestParseSignalmanAddrs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "host:1", []string{"host:1"}},
		{"three", "a:1,b:2,c:3", []string{"a:1", "b:2", "c:3"}},
		{"trims and skips blanks", "  a:1 , ,b:2,", []string{"a:1", "b:2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSignalmanAddrs(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseSignalmanAddrs(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseSignalmanAddrsByRegion(t *testing.T) {
	got := parseSignalmanAddrsByRegion("us-east=us1:1,us2:1;eu-west=eu1:1,eu2:1,eu3:1")
	want := map[string][]string{
		"us-east": {"us1:1", "us2:1"},
		"eu-west": {"eu1:1", "eu2:1", "eu3:1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSignalmanAddrsByRegion = %v, want %v", got, want)
	}
	if parseSignalmanAddrsByRegion("") != nil {
		t.Fatal("empty input should return nil")
	}
	if parseSignalmanAddrsByRegion("malformed") != nil {
		t.Fatal("malformed input should return nil")
	}
}

func TestRotateAddrsIsDeterministicPerTenant(t *testing.T) {
	addrs := []string{"a:1", "b:2", "c:3"}
	first := rotateAddrs(addrs, "tenant-A")
	second := rotateAddrs(addrs, "tenant-A")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same tenant should produce same order: %v vs %v", first, second)
	}
	if len(first) != 3 {
		t.Fatalf("rotateAddrs lost entries: %v", first)
	}
	// Distinct tenants don't all collide on the same head replica.
	seen := map[string]bool{}
	for _, tid := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		seen[rotateAddrs(addrs, tid)[0]] = true
	}
	if len(seen) < 2 {
		t.Fatalf("tenant rotation produced no spread across 8 tenants: %v", seen)
	}
}

func TestSubscriptionClientKeyKeepsTenantWhenAddressHasPort(t *testing.T) {
	key := subscriptionClientKey("user-1", "tenant-1", "signalman.internal:9095")
	if got := tenantIDFromSubscriptionClientKey(key); got != "tenant-1" {
		t.Fatalf("tenantIDFromSubscriptionClientKey = %q, want tenant-1", got)
	}
}

func TestAddrsForRegionPrefersMultiList(t *testing.T) {
	sm := &SubscriptionManager{
		signalmanAddr:       "local:1",
		signalmanAddrsLocal: []string{"local-a:1", "local-b:1"},
		signalmanAddrByRegion: map[string]string{
			"us-east": "us-single:1",
		},
		signalmanAddrsByRegion: map[string][]string{
			"us-east": {"us-a:1", "us-b:1", "us-c:1"},
		},
	}

	got := sm.addrsForRegion("us-east", "t1")
	if len(got) != 3 {
		t.Fatalf("us-east multi list should win: got %v", got)
	}

	got = sm.addrsForRegion("", "t1")
	if len(got) != 2 {
		t.Fatalf("empty region should use local multi list: got %v", got)
	}

	got = sm.addrsForRegion("nowhere", "t1")
	if len(got) != 2 {
		t.Fatalf("unknown region should use local multi list: got %v", got)
	}
}

func TestAddrsForRegionFallsBackToSingleWhenNoMulti(t *testing.T) {
	sm := &SubscriptionManager{
		signalmanAddr: "local:1",
		signalmanAddrByRegion: map[string]string{
			"us-east": "us:1",
		},
	}

	got := sm.addrsForRegion("us-east", "t1")
	if !reflect.DeepEqual(got, []string{"us:1"}) {
		t.Fatalf("us-east single should win when no multi: got %v", got)
	}

	got = sm.addrsForRegion("", "t1")
	if !reflect.DeepEqual(got, []string{"local:1"}) {
		t.Fatalf("empty region single fallback: got %v", got)
	}
}

func TestConnectionAddrsForStreamUsesOriginRegionReplicaList(t *testing.T) {
	sm := &SubscriptionManager{
		signalmanAddr:       "local:1",
		signalmanAddrsLocal: []string{"local-a:1", "local-b:1"},
		signalmanAddrsByRegion: map[string][]string{
			"us-east": {"us-a:1", "us-b:1", "us-c:1"},
		},
		streamOriginResolver: func(context.Context, string) (string, error) {
			return "us-east", nil
		},
	}

	got := sm.connectionAddrsForStream(context.Background(), "stream-1", "tenant-1")
	if len(got) != 3 {
		t.Fatalf("stream-origin lookup should return every regional replica, got %v", got)
	}
	for _, addr := range got {
		if !strings.HasPrefix(addr, "us-") {
			t.Fatalf("stream-origin list should use us-east replicas, got %v", got)
		}
	}
}

// TestGetOrCreateConnectionFromListNoAddrsConfigured proves the connector
// reports a clean error when no fallback is available.
func TestGetOrCreateConnectionFromListNoAddrsConfigured(t *testing.T) {
	sm := NewSubscriptionManager(logging.NewLoggerWithService("test"), SubscriptionManagerConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := sm.getOrCreateConnectionFromList(ctx, ConnectionConfig{UserID: "u", TenantID: "t"}, nil)
	if err == nil {
		t.Fatal("expected error when no addrs and no fallback configured")
	}
	if !strings.Contains(err.Error(), "no Signalman addresses configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}
