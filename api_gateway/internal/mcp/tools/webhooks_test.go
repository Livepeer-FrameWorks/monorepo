package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/resolvers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const webhookToolTenant = "7a1d0000-0000-4000-8000-0000000000c1"

func webhookToolSession(t *testing.T, bosun *clientstest.FakeBosun) *mcp.ClientSession {
	t.Helper()
	ctx := context.WithValue(clientstest.AuthedCtx(webhookToolTenant), ctxkeys.KeyUserID, "user")
	resolver := &resolvers.Resolver{Clients: clientstest.Clients(clientstest.WithBosun(bosun)), Logger: clientstest.DiscardLogger()}
	server := mcp.NewServer(&mcp.Implementation{Name: "webhook-contract", Version: "test"}, nil)
	RegisterWebhookTools(server, resolver)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "webhook-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// recordingBosun answers every webhook write and records which RPCs ran.
func recordingBosun(calls map[string]int) *clientstest.FakeBosun {
	endpoint := func() *bosunpb.WebhookEndpoint {
		return &bosunpb.WebhookEndpoint{Id: "ep-1", Url: "https://hooks.example.com", EventTypes: []string{"*"}, ApiVersion: "v1", CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}
	}
	delivery := &bosunpb.WebhookDelivery{Id: "dl-1", EndpointId: "ep-1", EventType: "stream.live", CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}
	return &clientstest.FakeBosun{
		CreateWebhookEndpointFn: func(context.Context, *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
			calls["create_webhook_endpoint"]++
			return &bosunpb.WebhookEndpointWithSecret{Endpoint: endpoint(), Secret: "whsec_c2VjcmV0"}, nil
		},
		UpdateWebhookEndpointFn: func(context.Context, *bosunpb.UpdateWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error) {
			calls["update_webhook_endpoint"]++
			return endpoint(), nil
		},
		DeleteWebhookEndpointFn: func(context.Context, string) (*bosunpb.DeleteWebhookEndpointResponse, error) {
			calls["delete_webhook_endpoint"]++
			return &bosunpb.DeleteWebhookEndpointResponse{EndpointId: "ep-1"}, nil
		},
		EnableWebhookEndpointFn: func(context.Context, string) (*bosunpb.WebhookEndpoint, error) {
			calls["enable_webhook_endpoint"]++
			return endpoint(), nil
		},
		DisableWebhookEndpointFn: func(context.Context, string) (*bosunpb.WebhookEndpoint, error) {
			calls["disable_webhook_endpoint"]++
			return endpoint(), nil
		},
		RotateWebhookEndpointSecretFn: func(context.Context, string, bool) (*bosunpb.WebhookEndpointWithSecret, error) {
			calls["rotate_webhook_secret"]++
			return &bosunpb.WebhookEndpointWithSecret{Endpoint: endpoint(), Secret: "whsec_bmV3"}, nil
		},
		TestWebhookEndpointFn: func(context.Context, string) (*bosunpb.TestWebhookEndpointResponse, error) {
			calls["test_webhook_endpoint"]++
			return &bosunpb.TestWebhookEndpointResponse{Delivery: delivery, Attempt: &bosunpb.WebhookDeliveryAttempt{Id: "at-1", StatusCode: 200, AttemptedAt: timestamppb.Now()}}, nil
		},
		ReplayWebhookDeliveryFn: func(context.Context, string) (*bosunpb.WebhookDelivery, error) {
			calls["replay_webhook_delivery"]++
			return delivery, nil
		},
		ReplayWebhookDeliveriesFn: func(context.Context, *bosunpb.ReplayWebhookDeliveriesRequest) (*bosunpb.ReplayWebhookDeliveriesResponse, error) {
			calls["replay_webhook_deliveries"]++
			return &bosunpb.ReplayWebhookDeliveriesResponse{ReplayedCount: 3}, nil
		},
	}
}

// Every webhook write tool refuses a call without its exact confirmation
// string before Bosun is called, and runs once the string is supplied.
func TestWebhookWriteToolsRequireConfirmation(t *testing.T) {
	writes := map[string]struct {
		args    map[string]any
		confirm string
	}{
		"create_webhook_endpoint":   {map[string]any{"url": "https://hooks.example.com", "event_types": []string{"*"}}, confirmCreateWebhookEndpoint},
		"update_webhook_endpoint":   {map[string]any{"endpoint_id": "ep-1", "description": "renamed"}, confirmUpdateWebhookEndpoint},
		"delete_webhook_endpoint":   {map[string]any{"endpoint_id": "ep-1"}, confirmDeleteWebhookEndpoint},
		"enable_webhook_endpoint":   {map[string]any{"endpoint_id": "ep-1"}, confirmEnableWebhookEndpoint},
		"disable_webhook_endpoint":  {map[string]any{"endpoint_id": "ep-1"}, confirmDisableWebhookEndpoint},
		"rotate_webhook_secret":     {map[string]any{"endpoint_id": "ep-1"}, confirmRotateWebhookSecret},
		"test_webhook_endpoint":     {map[string]any{"endpoint_id": "ep-1"}, confirmTestWebhookEndpoint},
		"replay_webhook_delivery":   {map[string]any{"delivery_id": "dl-1"}, confirmReplayWebhookDelivery},
		"replay_webhook_deliveries": {map[string]any{"endpoint_id": "ep-1", "created_after": "2026-09-01T00:00:00Z", "created_before": "2026-09-02T00:00:00Z"}, confirmReplayWebhookRange},
	}
	for name, policy := range ToolPolicies() {
		if strings.Contains(name, "webhook") && policy.Risk != ToolRiskRead {
			if _, ok := writes[name]; !ok {
				t.Fatalf("webhook write tool %s has no confirmation case", name)
			}
			if policy.Risk != ToolRiskHigh || policy.Scope != "developer:write" {
				t.Fatalf("%s policy = %+v, want high-risk developer:write", name, policy)
			}
		}
	}

	calls := map[string]int{}
	session := webhookToolSession(t, recordingBosun(calls))
	call := func(name string, args map[string]any) (*mcp.CallToolResult, error) {
		return session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	}
	for name, tc := range writes {
		for _, confirm := range []any{nil, "", "yes", strings.ToLower(tc.confirm)} {
			args := map[string]any{}
			for k, v := range tc.args {
				args[k] = v
			}
			if confirm != nil {
				args["confirm"] = confirm
			}
			result, err := call(name, args)
			if err == nil && (result == nil || !result.IsError) {
				t.Fatalf("%s with confirm=%v was not refused: %+v", name, confirm, result)
			}
			if calls[name] != 0 {
				t.Fatalf("%s with confirm=%v reached Bosun", name, confirm)
			}
		}

		args := map[string]any{"confirm": tc.confirm}
		for k, v := range tc.args {
			args[k] = v
		}
		result, err := call(name, args)
		if err != nil || result.IsError {
			t.Fatalf("%s with confirm=%q failed: %v %+v", name, tc.confirm, err, result)
		}
		if calls[name] != 1 {
			t.Fatalf("%s with confirm=%q made %d Bosun calls, want 1", name, tc.confirm, calls[name])
		}
	}
}

// create and rotate return the one-time secret with a warning; the read tool
// for the same endpoint returns no secret.
func TestWebhookSecretToolsReturnSecretOnce(t *testing.T) {
	calls := map[string]int{}
	bosun := recordingBosun(calls)
	bosun.GetWebhookEndpointFn = func(context.Context, string) (*bosunpb.WebhookEndpoint, error) {
		return &bosunpb.WebhookEndpoint{Id: "ep-1", CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}, nil
	}
	session := webhookToolSession(t, bosun)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "rotate_webhook_secret", Arguments: map[string]any{"endpoint_id": "ep-1", "confirm": confirmRotateWebhookSecret}})
	if err != nil || result.IsError {
		t.Fatalf("rotate: %v %+v", err, result)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(raw), "whsec_bmV3") || !strings.Contains(string(raw), "returned once") {
		t.Fatalf("rotate result = %s, want the secret and the warning", raw)
	}

	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_webhook_endpoint", Arguments: map[string]any{"endpoint_id": "ep-1"}})
	if err != nil || result.IsError {
		t.Fatalf("get: %v %+v", err, result)
	}
	raw, _ = json.Marshal(result.StructuredContent)
	if strings.Contains(string(raw), "whsec_") {
		t.Fatalf("get_webhook_endpoint leaked a secret: %s", raw)
	}
}
