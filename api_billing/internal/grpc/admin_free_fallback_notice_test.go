package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFallbackNoticeRetryRequiresInteractivePlatformOperator(t *testing.T) {
	for _, kind := range []string{"service", "api_token", "jwt"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, kind)
			ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator")
			if kind != "jwt" {
				ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
			}
			_, err := (&PurserServer{}).AdminResendFreeFallbackNotice(ctx, &purserpb.AdminResendFreeFallbackNoticeRequest{})
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("credential %s bypassed operator check: %v", kind, err)
			}
		})
	}
}
