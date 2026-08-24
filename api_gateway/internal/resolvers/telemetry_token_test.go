package resolvers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetrytoken"
)

func TestStampTelemetryToken(t *testing.T) {
	t.Parallel()

	secret := []byte("0123456789abcdef0123456789abcdef")
	tests := []struct {
		name            string
		primaryCluster  string
		metadataContent string
		wantCluster     string
		wantContent     string
	}{
		{
			name:            "remote endpoint preserves resolved cluster",
			primaryCluster:  "cluster-remote",
			metadataContent: "content-authoritative",
			wantCluster:     "cluster-remote",
			wantContent:     "content-authoritative",
		},
		{
			name:        "local endpoint uses resolver cluster and requested content",
			wantCluster: "cluster-local",
			wantContent: "content-requested",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := &Resolver{
				Logger:          logging.NewLogger(),
				TelemetrySecret: secret,
				LocalClusterID:  "cluster-local",
			}
			response := &sharedpb.ViewerEndpointResponse{
				Primary: &sharedpb.ViewerEndpoint{
					NodeId:    "node-1",
					ClusterId: tt.primaryCluster,
				},
				Metadata: &sharedpb.PlaybackMetadata{ContentId: tt.metadataContent},
			}

			resolver.stampTelemetryToken("content-requested", response)

			if response.Metadata.TelemetryToken == nil || *response.Metadata.TelemetryToken == "" {
				t.Fatal("expected telemetry token")
			}
			claims, err := telemetrytoken.Verify(secret, *response.Metadata.TelemetryToken, time.Now())
			if err != nil {
				t.Fatalf("verify telemetry token: %v", err)
			}
			if claims.ContentID != tt.wantContent {
				t.Errorf("content id = %q, want %q", claims.ContentID, tt.wantContent)
			}
			if claims.NodeID != "node-1" {
				t.Errorf("node id = %q, want node-1", claims.NodeID)
			}
			if claims.ServingClusterID != tt.wantCluster {
				t.Errorf("serving cluster id = %q, want %q", claims.ServingClusterID, tt.wantCluster)
			}
		})
	}
}
