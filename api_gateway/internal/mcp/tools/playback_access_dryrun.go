package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterPlaybackAccessTestTools registers the dry-run playback policy tester.
//
// SENSITIVITY: this tool accepts customer-supplied JWTs (potentially live
// viewer tokens) and, in webhook mode with fire_webhook=true, fires a real
// outbound HTTPS request to the customer-configured URL. Sensitivity flags
// live in docs/platform-features.yaml; the tool description repeats the
// warning at the point of use.
func RegisterPlaybackAccessTestTools(server *mcp.Server, serviceClients *clients.ServiceClients, _ *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name: "test_playback_access",
			Description: "Dry-run the playback policy evaluator against a JWT (or webhook test) without registering a viewer session. " +
				"For JWT policies: returns Allow/Deny + reason + kid + parsed claims. " +
				"For webhook policies: when fire_webhook=true, makes a real outbound HTTPS request to the customer URL. " +
				"Sensitive — never persist the JWT or webhook URL outside the operator-approved request.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args TestPlaybackAccessInput) (*mcp.CallToolResult, any, error) {
			return handleTestPlaybackAccess(ctx, args, serviceClients, checker, logger)
		},
	)
}

type TestPlaybackAccessInput struct {
	PlaybackID   string `json:"playback_id,omitempty" jsonschema_description:"Public playback identifier (one of playback_id or internal_name required)"`
	InternalName string `json:"internal_name,omitempty" jsonschema_description:"MistServer internal stream name (one of playback_id or internal_name required)"`
	ViewerToken  string `json:"viewer_token,omitempty" jsonschema_description:"JWT to test (required for type=jwt policies)"`
	ViewerIP     string `json:"viewer_ip,omitempty"`
	RequestURL   string `json:"request_url,omitempty"`
	Connector    string `json:"connector,omitempty" jsonschema_description:"Mist connector name (e.g. 'hls', 'webrtc'); used in the webhook payload"`
	SessionID    string `json:"session_id,omitempty"`
	FireWebhook  bool   `json:"fire_webhook,omitempty" jsonschema_description:"Webhook policies: when true, fires a real outbound HTTPS request to the customer URL"`
}

type TestPlaybackAccessResult struct {
	Allowed              bool   `json:"allowed"`
	PolicyType           string `json:"policy_type"`
	Reason               string `json:"reason,omitempty"`
	Detail               string `json:"detail,omitempty"`
	Kid                  string `json:"kid,omitempty"`
	ClaimsJSON           string `json:"claims_json,omitempty"`
	WebhookStatus        int32  `json:"webhook_status,omitempty"`
	WebhookLatencyMs     int32  `json:"webhook_latency_ms,omitempty"`
	ResolvedInternalName string `json:"resolved_internal_name,omitempty"`
}

func handleTestPlaybackAccess(ctx context.Context, args TestPlaybackAccessInput, c *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	switch {
	case args.PlaybackID == "" && args.InternalName == "":
		return toolError("exactly one of playback_id or internal_name is required")
	case args.PlaybackID != "" && args.InternalName != "":
		return toolError("exactly one of playback_id or internal_name may be provided")
	}
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.FireWebhook {
		if result, meta, err := requirePositiveBalance(ctx, checker); result != nil || meta != nil || err != nil {
			return result, meta, err
		}
	}

	resp, err := c.Commodore.TestPlaybackAccess(ctx, &foghorncontrolpb.TestPlaybackAccessRequest{
		PlaybackId:   args.PlaybackID,
		InternalName: args.InternalName,
		ViewerToken:  args.ViewerToken,
		ViewerIp:     args.ViewerIP,
		RequestUrl:   args.RequestURL,
		Connector:    args.Connector,
		SessionId:    args.SessionID,
		FireWebhook:  args.FireWebhook,
	})
	if err != nil {
		logger.WithError(err).Warn("test_playback_access failed")
		return toolError(fmt.Sprintf("test playback access: %v", err))
	}
	return toolSuccess(TestPlaybackAccessResult{
		Allowed:              resp.GetAllowed(),
		PolicyType:           resp.GetPolicyType(),
		Reason:               resp.GetReason(),
		Detail:               resp.GetDetail(),
		Kid:                  resp.GetKid(),
		ClaimsJSON:           resp.GetClaimsJson(),
		WebhookStatus:        resp.GetWebhookStatus(),
		WebhookLatencyMs:     resp.GetWebhookLatencyMs(),
		ResolvedInternalName: resp.GetResolvedInternalName(),
	})
}
