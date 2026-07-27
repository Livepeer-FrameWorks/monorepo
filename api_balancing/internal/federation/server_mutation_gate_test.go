package federation

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// The entire inbound federation surface is disabled as ONE policy by default: every RPC — including the reads
// QueryStream and PrepareArtifact — fails closed under valid service auth, so a shared-service-token holder
// cannot mutate, create, enumerate, drive routing, or read routing/artifact data for another cluster. There is
// no read-only allowlist.
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
