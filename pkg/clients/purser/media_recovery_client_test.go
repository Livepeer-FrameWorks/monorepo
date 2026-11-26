package purser

import (
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestMissingRecoveryClientFailsClosed(t *testing.T) {
	for _, client := range []*GRPCClient{nil, {}} {
		if _, err := client.GetMediaRecoveryStatus(t.Context(), "tenant"); status.Code(err) != codes.Unavailable {
			t.Fatalf("missing recovery client did not fail closed: %v", err)
		}
		if _, err := client.CommitMediaRecoveryExpiry(t.Context(), &purserpb.CommitMediaRecoveryExpiryRequest{TenantId: "tenant"}); status.Code(err) != codes.Unavailable {
			t.Fatalf("missing expiry client did not fail closed: %v", err)
		}
	}
}
