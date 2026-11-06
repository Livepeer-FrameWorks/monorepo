package grpcserver

import (
	"context"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
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

func TestDiagnosticWebhookURLRemovesPathCredentials(t *testing.T) {
	for _, raw := range []string{
		"https://hooks.slack.com/services/secret-team/secret-channel/secret-token",
		"https://discord.com/api/webhooks/secret-id/secret-token",
		"https://user:secret-password@hooks.example:8443/hook/secret-path?token=secret-query#secret-fragment",
	} {
		got := diagnosticEndpointURL(raw)
		if strings.Contains(got, "secret-") || strings.Contains(got, "/services/") || strings.Contains(got, "/webhooks/") {
			t.Fatalf("diagnostic endpoint leaked path credentials: %s", got)
		}
	}
}
