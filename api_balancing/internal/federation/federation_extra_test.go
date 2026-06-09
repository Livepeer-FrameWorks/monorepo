package federation

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

// GetAllRemoteEdges SCANs every cached peer edge under this cluster's namespace
// (the 60s repair/debug sweep relies on it). Entries written for different peers
// all surface.
func TestGetAllRemoteEdges(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	for _, peer := range []string{"cluster-b", "cluster-c"} {
		entry := &RemoteEdgeEntry{
			StreamName: "tenant1+stream" + peer,
			NodeID:     "node-" + peer,
			BaseURL:    "edge.example.com",
			UpdatedAt:  time.Now().Unix(),
		}
		if err := cache.SetRemoteEdge(ctx, peer, entry); err != nil {
			t.Fatalf("set edge %s: %v", peer, err)
		}
	}

	all, err := cache.GetAllRemoteEdges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d edges, want 2", len(all))
	}
}

// GetAllRemoteArtifacts SCANs every cached remote artifact across all peer
// prefixes (covers keyRemoteArtifactGlob).
func TestGetAllRemoteArtifacts(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	if err := cache.SetRemoteArtifact(ctx, "cluster-b", &RemoteArtifactEntry{ArtifactHash: "h1", NodeID: "n1", ArtifactType: "vod"}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SetRemoteArtifact(ctx, "cluster-c", &RemoteArtifactEntry{ArtifactHash: "h2", NodeID: "n2", ArtifactType: "clip"}); err != nil {
		t.Fatal(err)
	}

	all, err := cache.GetAllRemoteArtifacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d artifacts, want 2", len(all))
	}
}

// federationContext blanks the user JWT so the client interceptor falls through
// to the service token for service-to-service federation RPCs — a viewer's JWT
// must never ride a cross-cluster call.
func TestFederationContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt")
	out := federationContext(ctx)
	if v, _ := out.Value(ctxkeys.KeyJWTToken).(string); v != "" {
		t.Fatalf("federationContext must blank the JWT, got %q", v)
	}
}
