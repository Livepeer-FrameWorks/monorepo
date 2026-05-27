package handlers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/federation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestActiveReplicationSource(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cache := federation.NewRemoteEdgeCache(client, "cluster-us", logging.NewLogger())

	oldCache := remoteEdgeCache
	oldLogger := logger
	remoteEdgeCache = cache
	logger = logging.NewLogger()
	t.Cleanup(func() {
		remoteEdgeCache = oldCache
		logger = oldLogger
		_ = client.Close()
		mr.Close()
	})

	const streamName = "frameworks-demo"
	const sourceURL = "dtsc://edge-eu-1.media-eu-1.frameworks.network:4200/frameworks-demo"
	if err := cache.SetActiveReplication(context.Background(), &federation.ActiveReplicationRecord{
		StreamName:    streamName,
		SourceNodeID:  "edge-eu-1",
		SourceCluster: "media-eu-1",
		DestCluster:   "media-us-1",
		DestNodeID:    "edge-us-1",
		DTSCURL:       sourceURL,
		BaseURL:       "edge-us-1.media-us-1.frameworks.network",
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("SetActiveReplication: %v", err)
	}

	got, ok := activeReplicationSource(context.Background(), streamName)
	if !ok {
		t.Fatal("expected active replication source")
	}
	if got != sourceURL {
		t.Fatalf("source = %q, want %q", got, sourceURL)
	}

	if got, ok := activeReplicationSource(context.Background(), "other-stream"); ok || got != "" {
		t.Fatalf("unexpected source for other stream: %q ok=%v", got, ok)
	}
}
