//go:build schema_verify

package integration

import (
	"context"
	"frameworks/api_webhooks/internal/grpcserver"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	"google.golang.org/protobuf/encoding/protojson"
	"strings"
	"testing"
)

func TestWebhookDiagnosisTenantAndURLRedaction_RealPG(t *testing.T) {
	store := newStore(t, startPostgres(t))
	tenant, other := newTenant(), newTenant()
	endpoint := createEndpoint(t, store, tenant, "https://user:secret-password@hooks.example/hook/secret-path?token=secret-query#secret-fragment")
	createEndpoint(t, store, other, "https://other.example/hook")
	ctx := context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	out, err := (&grpcserver.Server{Store: store}).DiagnoseWebhooks(ctx, &bosunpb.DiagnoseWebhooksRequest{TenantId: tenant})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.GetEndpoints()) != 1 || out.GetEndpoints()[0].GetEndpoint().GetId() != endpoint.ID || out.GetEndpoints()[0].GetEndpoint().GetUrl() != "https://hooks.example" {
		t.Fatalf("wrong endpoint: %v", out)
	}
	body, err := protojson.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret-") || strings.Contains(string(body), "other.example") {
		t.Fatalf("diagnosis leaked URL credentials or another tenant: %s", body)
	}
}
