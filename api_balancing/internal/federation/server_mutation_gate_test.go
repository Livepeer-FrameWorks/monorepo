package federation

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// The entire inbound federation surface is disabled as one policy by default. The production entrypoint opts
// into the whole surface only when FEDERATION_ENABLED is true; focused/default server construction stays closed.
func TestFederationMutations_DisabledByDefault(t *testing.T) {
	// AllowFederationMutations omitted → false.
	srv := NewFederationServer(FederationServerConfig{Logger: logging.NewLogger(), ClusterID: "platform-eu"})
	ctx := serviceAuthContext()

	t.Run("MintStorageURLs", func(t *testing.T) {
		resp, err := srv.MintStorageURLs(ctx, &foghornfederationpb.MintStorageURLsRequest{
			TenantId: "t", TargetClusterId: "platform-eu", ArtifactType: "thumbnail", ArtifactKey: "k",
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if resp.GetAccepted() || resp.GetReason() != "federation_mutations_disabled" {
			t.Fatalf("want disabled, got accepted=%v reason=%q", resp.GetAccepted(), resp.GetReason())
		}
	})

	t.Run("NotifyOriginPull", func(t *testing.T) {
		ack, err := srv.NotifyOriginPull(ctx, &foghornfederationpb.OriginPullNotification{StreamName: "s"})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if ack.GetAccepted() || ack.GetReason() != "federation_mutations_disabled" {
			t.Fatalf("want disabled, got accepted=%v reason=%q", ack.GetAccepted(), ack.GetReason())
		}
	})

	t.Run("CreateRemoteClip", func(t *testing.T) {
		_, err := srv.CreateRemoteClip(ctx, &foghornfederationpb.RemoteClipRequest{StreamInternalName: "s", TenantId: "t"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("want FailedPrecondition, got %v", err)
		}
	})

	t.Run("CreateRemoteDVR", func(t *testing.T) {
		_, err := srv.CreateRemoteDVR(ctx, &foghornfederationpb.RemoteDVRRequest{StreamInternalName: "s", TenantId: "t"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("want FailedPrecondition, got %v", err)
		}
	})

	t.Run("ListTenantArtifacts", func(t *testing.T) {
		_, err := srv.ListTenantArtifacts(ctx, &foghornfederationpb.ListTenantArtifactsRequest{TenantId: "t"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("want FailedPrecondition, got %v", err)
		}
	})

	t.Run("PeerChannel", func(t *testing.T) {
		// Service-auth stream, but mutations disabled → fail closed before any message is read.
		err := srv.PeerChannel(&mockPeerChannelStream{ctx: serviceAuthContext()})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("want FailedPrecondition, got %v", err)
		}
	})

	// No read-only allowlist: even QueryStream (routing data) and PrepareArtifact (presigned URL / relay grant)
	// fail closed under the shared service token.
	t.Run("QueryStream", func(t *testing.T) {
		_, err := srv.QueryStream(ctx, &foghornfederationpb.QueryStreamRequest{StreamName: "s", TenantId: "t"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("want FailedPrecondition, got %v", err)
		}
	})

	t.Run("PrepareArtifact", func(t *testing.T) {
		_, err := srv.PrepareArtifact(ctx, &foghornfederationpb.PrepareArtifactRequest{ArtifactId: "h", TenantId: "t"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("want FailedPrecondition, got %v", err)
		}
	})
}

func TestFederationMutations_EnabledForProviderFederation(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{
		Logger: logging.NewLogger(), ClusterID: "platform-eu", AllowFederationMutations: true,
	})

	// Crossing the gate reaches the RPC's own validation instead of the federation-disabled result.
	_, err := srv.QueryStream(serviceAuthContext(), &foghornfederationpb.QueryStreamRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("enabled provider federation did not reach QueryStream validation: %v", err)
	}

	// Enabling federation does not weaken the service-auth boundary.
	_, err = srv.QueryStream(context.Background(), &foghornfederationpb.QueryStreamRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("enabled provider federation accepted an unauthenticated request: %v", err)
	}
}
