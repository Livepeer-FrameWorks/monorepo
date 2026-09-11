package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func placementToolSession(t *testing.T, ctx context.Context, resolver *resolvers.Resolver) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "placement-contract", Version: "test"}, nil)
	RegisterMediaPlacementTools(server, resolver)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "placement-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func placementToolCall(t *testing.T, session *mcp.ClientSession, name string, args any, wantError bool, wantType string) json.RawMessage {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError != wantError || result.StructuredContent == nil || len(result.Content) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(encoded, &output); err != nil || output.Type != wantType {
		t.Fatalf("output = %s, error = %v", encoded, err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !json.Valid([]byte(text.Text)) {
		t.Fatal("missing JSON text fallback")
	}
	return output.Value
}

func TestPlacementMCPApplyAndRecoverPreservesReviewedIntent(t *testing.T) {
	ctx := context.WithValue(clientstest.AuthedCtx("tenant"), ctxkeys.KeyUserID, "user")
	var saved *placementpb.ApplyChangeRequest
	applyCalls := 0
	fake := &clientstest.FakeCommodore{
		ApplyMediaPlacementChangeFn: func(ctx context.Context, req *placementpb.ApplyChangeRequest) (*placementpb.Change, error) {
			applyCalls++
			if ctxkeys.GetTenantID(ctx) != "tenant" || ctxkeys.GetUserID(ctx) != "user" {
				t.Fatal("lost authenticated identity")
			}
			saved = proto.CloneOf(req)
			return nil, status.Error(codes.DeadlineExceeded, "private backend diagnostic")
		},
		GetMediaPlacementChangeFn: func(_ context.Context, req *placementpb.GetChangeRequest) (*placementpb.Change, error) {
			if saved == nil || req.GetIdempotencyKey() != saved.GetIdempotencyKey() || !proto.Equal(req.GetScope(), saved.GetChange().GetScope()) {
				t.Fatal("recovery changed command identity")
			}
			return &placementpb.Change{Scope: req.Scope, IdempotencyKey: req.IdempotencyKey, Revision: 9007199254740994, ParentRevision: 7, Digest: "digest", CreatedAt: timestamppb.Now(), Rollout: &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING}}, nil
		},
	}
	session := placementToolSession(t, ctx, &resolvers.Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))})
	scope := map[string]any{"kind": "STREAM", "streamId": "stream"}
	args := map[string]any{"scope": scope, "expectedRevision": "9007199254740993", "expectedParentRevision": "7", "updates": []any{map[string]any{"verb": "SERVE", "kind": "CLEAR"}}, "reviewToken": "review-token", "idempotencyKey": "same-key", "acknowledgedWarningIds": []string{"warning-id"}}
	out := placementToolCall(t, session, "apply_media_placement_change", args, true, "MediaPlacementError")
	if saved == nil || saved.GetChange().GetExpectedRevision() != 9007199254740993 || saved.GetChange().GetExpectedParentRevision() != 7 || saved.GetReviewToken() != "review-token" || len(saved.GetAcknowledgedWarningIds()) != 1 || saved.GetAcknowledgedWarningIds()[0] != "warning-id" || saved.GetChange().GetUpdates()[0].GetKind() != placementpb.UpdateKind_UPDATE_KIND_CLEAR {
		t.Fatalf("reviewed intent changed: %v", saved)
	}
	if strings.Contains(string(out), "private backend") || !strings.Contains(string(out), "UNAVAILABLE") {
		t.Fatalf("unsafe or untyped error: %s", out)
	}
	out = placementToolCall(t, session, "get_media_placement_change", map[string]any{"scope": scope, "idempotencyKey": "same-key"}, false, "MediaPlacementChange")
	var change model.MediaPlacementChange
	if err := json.Unmarshal(out, &change); err != nil || change.Revision != "9007199254740994" || change.Rollout.Status != model.MediaPlacementRolloutStatusPending || applyCalls != 1 {
		t.Fatalf("receipt precision/rollout/retry failure: %s, %v", out, err)
	}
}

func TestPlacementMCPRequiresScopedIdentityAndRejectsUnknownFields(t *testing.T) {
	for _, scenario := range []string{"anonymous", "read scope missing", "member write", "demo"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.WithValue(clientstest.AuthedCtx("tenant"), ctxkeys.KeyUserID, "user")
			name, wantType := "get_media_placement_policy", "AuthError"
			wantError := true
			args := map[string]any{"scope": map[string]any{"kind": "TENANT"}}
			switch scenario {
			case "anonymous":
				ctx = context.Background()
			case "read scope missing":
				ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
				ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
			case "member write":
				ctx = context.WithValue(ctx, ctxkeys.KeyRole, "member")
				name = "review_media_placement_change"
				args["expectedRevision"], args["expectedParentRevision"], args["updates"] = "0", "0", []any{}
			case "demo":
				ctx = context.WithValue(ctx, ctxkeys.KeyDemoMode, true)
				wantType, wantError = "MediaPlacementPolicyState", false
			}
			fake := &clientstest.FakeCommodore{}
			session := placementToolSession(t, ctx, &resolvers.Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))})
			output := placementToolCall(t, session, name, args, wantError, wantType)
			if scenario == "demo" {
				var policy model.MediaPlacementPolicyState
				if err := json.Unmarshal(output, &policy); err != nil || policy.Actions == nil || policy.Actions.CanManage || policy.ActiveRevision != nil || !strings.Contains(string(output), "Simulated") {
					t.Fatalf("demo MCP policy claimed live state: %s, %v", output, err)
				}
				placementToolCall(t, session, "apply_media_placement_change", map[string]any{
					"scope": args["scope"], "expectedRevision": "0", "expectedParentRevision": "0", "updates": []any{},
					"reviewToken": "demo-preview-only", "idempotencyKey": "demo-change", "acknowledgedWarningIds": []string{},
				}, true, "MediaPlacementError")
			}
			args["tenantId"] = "other-tenant"
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
			if err == nil && !result.IsError {
				t.Fatal("accepted caller-controlled tenant field")
			}
			if fake.Calls != 0 {
				t.Fatal("unauthorized or demo request reached backend")
			}
		})
	}
}

func TestPlacementMCPPoliciesKeepRecoveryFreeAndApplyHighRisk(t *testing.T) {
	for name, policy := range ToolPolicies() {
		if !strings.HasPrefix(policy.Scope, "placement:") {
			continue
		}
		if policy.Public || !policy.AccessClass.UnfundedAllowed() {
			t.Fatalf("placement management is public or requires funded balance: %s", name)
		}
		if strings.HasPrefix(name, "apply_") && (policy.Risk != ToolRiskHigh || !policy.Idempotent || !policy.Destructive) {
			t.Fatalf("apply policy lost safety metadata: %s", name)
		}
		if strings.HasPrefix(name, "review_") && (policy.Scope != "placement:write" || policy.Risk != ToolRiskRead) {
			t.Fatalf("review permission or read-only annotation changed: %s", name)
		}
	}
}

func TestPlacementMCPDiscoveryRejectsInvalidEnumsAndBounds(t *testing.T) {
	session := placementToolSession(t, context.Background(), nil)
	for _, args := range []map[string]any{
		{"scope": map[string]any{"kind": "OTHER"}},
		{"scope": map[string]any{"kind": "TENANT", "tenantId": "other"}},
		{"scope": map[string]any{"kind": "TENANT"}, "first": 101},
		{"scope": map[string]any{"kind": "TENANT"}, "filter": map[string]any{"kind": "SECRET"}},
		{"scope": map[string]any{"kind": "TENANT"}, "filter": map[string]any{"classes": []string{"UNKNOWN"}}},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_media_placement_options", Arguments: args})
		if err == nil && (result == nil || !result.IsError || result.StructuredContent != nil) {
			t.Fatalf("invalid discovery input reached resolver instead of schema rejection: args=%v result=%+v", args, result)
		}
	}
}
