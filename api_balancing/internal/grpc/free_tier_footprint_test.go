package grpc

import (
	"context"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestTenantMediaFootprintRequiresService(t *testing.T) {
	s := &FoghornGRPCServer{}
	for _, auth := range []string{"", "jwt", "api_token"} {
		ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, auth)
		if _, err := s.GetTenantMediaFootprint(ctx, &foghornpb.GetTenantMediaFootprintRequest{TenantId: "tenant"}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("auth %q read internal resources: %v", auth, err)
		}
	}
}
