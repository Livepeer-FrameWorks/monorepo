package control

import (
	"context"
	"testing"
	"time"
)

func TestInboundPlacementDemandFollowsExactPhysicalAttempt(t *testing.T) {
	ctx := context.Background()
	registry := NewStreamRegistry(nil, "cell", time.Minute)
	pull, err := registry.RecordInboundPull(ctx, "stream", testInbound("edge"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mismatch := range []string{"tenant", "attempt", "destination", "source", "generation", "url"} {
		expected := pull
		switch mismatch {
		case "tenant":
			expected.TenantID = "foreign"
		case "attempt":
			expected.AttemptID = "old"
		case "destination":
			expected.DestClusterID = "foreign"
		case "source":
			expected.SourceNodeID = "foreign"
		case "generation":
			expected.SourceGeneration = "old"
		case "url":
			expected.DTSCURL = "dtsc://foreign/stream"
		}
		if err = registry.RecordInboundPlacementDemand(ctx, "stream", expected, "context"); err == nil {
			t.Fatalf("%s changed retained demand", mismatch)
		}
		if err = registry.RequireInboundPlacement(ctx, "stream", expected); err == nil {
			t.Fatalf("%s changed placement requirement", mismatch)
		}
	}
	if err = registry.RecordInboundPlacementDemand(ctx, "stream", pull, "context"); err != nil {
		t.Fatal(err)
	}
	warm, err := registry.RecordInboundPull(ctx, "stream", testInbound("edge"))
	if err != nil || warm.PlacementDemand != "context" || !warm.PlacementRequired || warm.AttemptID != pull.AttemptID {
		t.Fatal("warm pull discarded request context")
	}
	if _, err = registry.ClearInboundPull(ctx, "stream", "edge", pull.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = registry.RecordInboundPlacementDemand(ctx, "stream", pull, "late"); err == nil {
		t.Fatal("cleared pull accepted late request context")
	}
	next := testInbound("edge")
	next.SourceGeneration, next.SourceRevision = "next", 2
	replacement, err := registry.RecordInboundPull(ctx, "stream", next)
	if err != nil || replacement.PlacementDemand != "" || replacement.PlacementRequired {
		t.Fatal("replacement inherited old viewer context")
	}
}

func TestInboundPlacementDemandPersistsAcrossReplicasAndClear(t *testing.T) {
	store, _, server := newTestRedis(t)
	ctx := context.Background()
	writer := NewStreamRegistry(nil, "cluster-test", time.Minute)
	writer.redisStore, writer.instanceID = store, "writer"
	pull, err := writer.RecordInboundPull(ctx, "stream", testInbound("edge"))
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.RequireInboundPlacement(ctx, "stream", pull); err != nil {
		t.Fatal(err)
	}
	marked, _, err := store.GetSource(ctx, "stream")
	if err != nil || !marked.Locations["cluster-test"].InboundPulls["edge"].PlacementRequired || marked.Locations["cluster-test"].InboundPulls["edge"].PlacementDemand != "" {
		t.Fatal("admission marker was not durable before request-context retention")
	}
	if err = writer.RecordInboundPlacementDemand(ctx, "stream", pull, "request-context"); err != nil {
		t.Fatal(err)
	}
	reader := NewStreamRegistry(nil, "cluster-test", time.Minute)
	reader.redisStore, reader.instanceID = store, "reader"
	current, found, err := reader.CurrentInboundPull(ctx, "stream", "edge")
	if err != nil || !found || current.PlacementDemand != "request-context" || !current.PlacementRequired {
		t.Fatalf("replica lost demand: %+v, %v", current, err)
	}
	if _, err = reader.ClearInboundPull(ctx, "stream", "edge", pull.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = writer.RecordInboundPlacementDemand(ctx, "stream", pull, "late-context"); err == nil {
		t.Fatal("stale writer restored cleared request context")
	}
	entry, _, err := store.GetSource(ctx, "stream")
	if err != nil || entry.Locations["cluster-test"].InboundPulls["edge"].PlacementDemand != "" {
		t.Fatal("cleared tombstone retained viewer context")
	}
	server.Close()
	bounded, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err = writer.RecordInboundPlacementDemand(bounded, "stream", pull, "offline-context"); err == nil {
		t.Fatal("failed persistence used a local fallback")
	}
}
