package federation

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateRemoteDVRRequiresOwnerRecordingAPI(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	if err := state.DefaultManager().UpdateStreamFromBuffer("stream-a", "stream-a", "node-a", "tenant-origin", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	srv := NewFederationServer(FederationServerConfig{Logger: testLogger(), AllowFederationMutations: true})
	for _, tenantID := range []string{"tenant-origin", "tenant-other", ""} {
		t.Run(tenantID, func(t *testing.T) {
			response, err := srv.CreateRemoteDVR(serviceAuthContext(), &foghornfederationpb.RemoteDVRRequest{
				StreamInternalName: "stream-a", TenantId: tenantID,
			})
			if status.Code(err) != codes.Unimplemented || response != nil {
				t.Fatalf("incomplete federation start must require the owner recording API, got response=%v err=%v", response, err)
			}
		})
	}
}
