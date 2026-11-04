package grpcserver

import (
	"context"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestDiagnoseWebhooksAuthorization(t *testing.T) {
	for _, auth := range []string{"service", "api_token", "jwt"} {
		ctx := context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, auth), ctxkeys.KeyUserID, "caller")
		if _, err := (&Server{}).DiagnoseWebhooks(ctx, &bosunpb.DiagnoseWebhooksRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s: %v", auth, err)
		}
	}
	ctx := context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "op"), ctxkeys.KeyPlatformOperator, true)
	if _, err := (&Server{}).DiagnoseWebhooks(ctx, &bosunpb.DiagnoseWebhooksRequest{TenantId: "bad"}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}
