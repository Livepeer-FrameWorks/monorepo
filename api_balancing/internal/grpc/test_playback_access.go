package grpc

import (
	"context"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/triggers"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestPlaybackAccess runs the same evaluator that USER_NEW uses, but against
// a caller-supplied JWT (or webhook test request) and without registering a
// viewer session. Tenant ownership of the playback target is validated
// upstream by Commodore.TestPlaybackAccess; this handler trusts the
// (tenant_id, playback_id|internal_name) pair and just executes the policy.
//
// JWT mode never has side effects — a token is parsed and verified locally
// against the policy's active keys.
//
// Webhook mode (fire_webhook=true and policy.type=webhook) does fire a
// real outbound HTTPS request to the customer URL. The same SSRF-hardened
// dialer used by the live USER_NEW path applies (see
// triggers/playback_auth.go newSSRFHardenedClient). Customer URLs that
// resolve to private/loopback/CGNAT addresses are blocked at dial time.
//
// fire_webhook=false on a webhook policy returns "webhook-test-skipped"
// without making the call so an operator can inspect the policy shape
// before opting in to the side effect.
func (s *FoghornGRPCServer) TestPlaybackAccess(ctx context.Context, req *foghorncontrolpb.TestPlaybackAccessRequest) (*foghorncontrolpb.TestPlaybackAccessResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required (Commodore facade resolves playback_id → internal_name)")
	}
	if req.GetPlaybackId() != "" {
		return nil, status.Error(codes.InvalidArgument, "playback_id must be cleared by the Commodore facade before reaching Foghorn")
	}

	if control.CommodoreClient == nil {
		return nil, status.Error(codes.FailedPrecondition, "commodore client not configured")
	}

	// Resolve the policy. Use the for-enforcement variant so we get the
	// decrypted webhook secret — same code path the live USER_NEW handler uses.
	policy, err := control.CommodoreClient.ResolvePlaybackPolicyByInternalName(ctx, req.GetInternalName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve policy: %v", err)
	}
	if policy == nil {
		return nil, status.Error(codes.NotFound, "policy not found for target")
	}

	resolvedInternal := req.GetInternalName()

	// Webhook policy with fire_webhook=false: short-circuit so the operator
	// gets the policy shape without paying the outbound HTTP side effect.
	// Allowed=false here is informational, not a real enforcement deny.
	if policy.GetType() == "webhook" && !req.GetFireWebhook() {
		return &foghorncontrolpb.TestPlaybackAccessResponse{
			Allowed:              false,
			PolicyType:           "webhook",
			Reason:               "webhook-test-skipped",
			Detail:               "set fire_webhook=true to actually call the customer endpoint",
			ResolvedInternalName: resolvedInternal,
		}, nil
	}

	userNew := &ipcpb.ViewerConnectTrigger{
		StreamName:  resolvedInternal,
		SessionId:   req.GetSessionId(),
		Host:        req.GetViewerIp(),
		RequestUrl:  req.GetRequestUrl(),
		ViewerToken: req.GetViewerToken(),
		Connector:   req.GetConnector(),
	}

	// recorder=nil — dry-run must not record a successful key use against
	// the audit table; live traffic is the only thing that should mark a
	// key as "in use today".
	d := triggers.EvaluatePlaybackPolicyDetailed(ctx, s.logger, resolvedInternal, userNew, policy, nil)
	if d == nil {
		return nil, status.Error(codes.Internal, "evaluator returned nil decision")
	}
	return &foghorncontrolpb.TestPlaybackAccessResponse{
		Allowed:              d.Allowed,
		PolicyType:           d.PolicyType,
		Reason:               d.Reason,
		Detail:               d.Detail,
		Kid:                  d.Kid,
		ClaimsJson:           d.ClaimsJSON,
		WebhookStatus:        int32(d.WebhookStatus),
		WebhookLatencyMs:     int32(d.WebhookLatencyMs),
		ResolvedInternalName: resolvedInternal,
	}, nil
}
