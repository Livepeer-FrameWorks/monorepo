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
// real outbound HTTPS request to the customer URL, through the same webhook
// client as the live USER_NEW path (triggers/playback_auth.go
// newPlaybackWebhookClient): the shared webhook destination policy refuses a
// connection to any non-public address at dial time.
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

	// The tester evaluates as a viewer whose headers were observed: the
	// supplied origin/referer, or none, as a request without them would carry.
	origin, referer := req.GetOrigin(), req.GetReferer()
	userNew := &ipcpb.ViewerConnectTrigger{
		StreamName:  resolvedInternal,
		SessionId:   req.GetSessionId(),
		Host:        req.GetViewerIp(),
		RequestUrl:  req.GetRequestUrl(),
		ViewerToken: req.GetViewerToken(),
		Connector:   req.GetConnector(),
		Origin:      &origin,
		Referer:     &referer,
	}

	// The origin rule runs first, as in live evaluation, so a disallowed origin
	// is reported without firing the webhook.
	if d := triggers.CheckPlaybackOrigin(s.logger, resolvedInternal, userNew, policy); d != nil {
		if d.Reason == "origin-missing" {
			d.Detail = "the policy restricts origins and no origin or referer was supplied"
		}
		return &foghorncontrolpb.TestPlaybackAccessResponse{
			Allowed: false, PolicyType: d.PolicyType, Reason: d.Reason, Detail: d.Detail, ResolvedInternalName: resolvedInternal,
		}, nil
	}

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
