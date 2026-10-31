package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOperatorGrantListRequiresOperatorSession(t *testing.T) {
	for _, authType := range []string{"", "service", "api_token", "jwt"} {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, authType)
		_, err := (&PurserServer{}).AdminListBillingGrants(ctx, &purserpb.AdminListBillingGrantsRequest{})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%q list auth=%v", authType, err)
		}
	}
}

func TestOperatorGrantListBoundsExpiryWindowBeforeStorage(t *testing.T) {
	ctx := context.WithValue(context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	_, err := (&PurserServer{}).AdminListBillingGrants(ctx, &purserpb.AdminListBillingGrantsRequest{ExpiringWithinSeconds: 1 << 62})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("huge expiry window=%v, want InvalidArgument", err)
	}
}
