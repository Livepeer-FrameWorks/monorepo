package resolvers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Webhook secrets are never returned by GraphQL queries — we render a literal
// placeholder so clients can tell a secret is configured without exposing it.
const webhookSecretMask = "redacted"

// ----------------------------------------------------------------------------
// Mutations
// ----------------------------------------------------------------------------

// DoCreateSigningKey generates a new ES256 keypair via Commodore and returns
// the private key exactly once.
func (r *Resolver) DoCreateSigningKey(ctx context.Context, input model.CreateSigningKeyInput) (model.CreateSigningKeyResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return &model.ValidationError{Message: "name is required"}, nil
	}

	resp, err := r.Clients.Commodore.CreateSigningKey(ctx, name)
	if err != nil {
		if mr := mapCommodoreErr(err); mr != nil {
			if v, ok := mr.(model.CreateSigningKeyResult); ok {
				return v, nil
			}
		}
		r.Logger.WithError(err).Error("CreateSigningKey gRPC failed")
		return nil, fmt.Errorf("create signing key: %w", err)
	}

	return &model.CreateSigningKeySuccess{
		SigningKey:    resp.GetSigningKey(),
		PrivateKeyPem: resp.GetPrivateKeyPem(),
	}, nil
}

// DoRevokeSigningKey marks a key revoked. Triggers cache+session invalidation
// in Commodore.
func (r *Resolver) DoRevokeSigningKey(ctx context.Context, id string) (model.RevokeSigningKeyResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return &model.NotFoundError{Message: "id is required"}, nil
	}

	sk, err := r.Clients.Commodore.RevokeSigningKey(ctx, id)
	if err != nil {
		if mr := mapCommodoreErr(err); mr != nil {
			if v, ok := mr.(model.RevokeSigningKeyResult); ok {
				return v, nil
			}
		}
		r.Logger.WithError(err).Error("RevokeSigningKey gRPC failed")
		return nil, fmt.Errorf("revoke signing key: %w", err)
	}
	return sk, nil
}

// DoSetPlaybackPolicy validates the input shape and forwards to Commodore,
// which persists, drops Foghorn caches, and triggers MistServer
// invalidate_sessions in the correct order.
func (r *Resolver) DoSetPlaybackPolicy(ctx context.Context, input model.SetPlaybackPolicyInput) (model.SetPlaybackPolicyResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if input.Policy == nil {
		return &model.ValidationError{Message: "policy is required"}, nil
	}

	target, vErr := pickPlaybackPolicyTarget(input)
	if vErr != nil {
		return vErr, nil
	}

	req := &pb.SetPlaybackPolicyRequest{}
	switch target.kind {
	case "stream":
		req.StreamId = target.id
	case "vod_asset":
		req.VodAssetId = target.id
	case "clip":
		req.ClipId = target.id
	}

	policyType, err := protoPolicyType(input.Policy.Type)
	if err != nil {
		// Surface as a GraphQL union ValidationError, not a Go error —
		// the schema's union-error pattern is the canonical way to
		// communicate user-input issues to the client.
		return &model.ValidationError{Message: err.Error()}, nil //nolint:nilerr
	}
	req.Type = policyType

	switch input.Policy.Type {
	case model.PlaybackPolicyTypePublic:
		// nothing else to send.
	case model.PlaybackPolicyTypeJwt:
		if input.Policy.Jwt == nil {
			return &model.ValidationError{Message: "jwt block required when type is JWT"}, nil
		}
		req.Jwt = &pb.PlaybackJwtPolicy{
			AllowedKids:        input.Policy.Jwt.AllowedKids,
			RequiredAudience:   input.Policy.Jwt.RequiredAudience,
			RequiredClaimsJson: claimReqsToProto(input.Policy.Jwt.RequiredClaimsJSON),
		}
	case model.PlaybackPolicyTypeWebhook:
		if input.Policy.Webhook == nil {
			return &model.ValidationError{Message: "webhook block required when type is WEBHOOK"}, nil
		}
		timeout := int32(0)
		if input.Policy.Webhook.TimeoutMs != nil {
			timeout = int32(*input.Policy.Webhook.TimeoutMs)
		}
		req.Webhook = &pb.PlaybackWebhookPolicy{
			Url:       input.Policy.Webhook.URL,
			TimeoutMs: timeout,
			SecretPt:  input.Policy.Webhook.Secret,
		}
	}

	resp, err := r.Clients.Commodore.SetPlaybackPolicy(ctx, req)
	if err != nil {
		if mr := mapCommodoreErr(err); mr != nil {
			if v, ok := mr.(model.SetPlaybackPolicyResult); ok {
				return v, nil
			}
		}
		r.Logger.WithError(err).Error("SetPlaybackPolicy gRPC failed")
		return nil, fmt.Errorf("set playback policy: %w", err)
	}

	// Resolve the updated object back so the response is convenient for callers.
	switch {
	case resp.GetStreamId() != "":
		stream, err := r.Clients.Commodore.GetStream(ctx, resp.GetStreamId())
		if err != nil {
			r.Logger.WithError(err).Warn("SetPlaybackPolicy: failed to refetch stream")
			return nil, fmt.Errorf("fetch stream after policy update: %w", err)
		}
		return stream, nil
	case resp.GetVodAssetId() != "":
		asset, err := r.DoGetVodAsset(ctx, resp.GetVodAssetId())
		if err != nil {
			r.Logger.WithError(err).Warn("SetPlaybackPolicy: failed to refetch VOD asset")
			return nil, fmt.Errorf("fetch VOD asset after policy update: %w", err)
		}
		if asset == nil {
			return &model.NotFoundError{Message: "VOD asset not found after policy update"}, nil
		}
		return asset, nil
	case resp.GetClipId() != "":
		clip, err := r.DoGetClip(ctx, resp.GetClipId())
		if err != nil {
			r.Logger.WithError(err).Warn("SetPlaybackPolicy: failed to refetch clip")
			return nil, fmt.Errorf("fetch clip after policy update: %w", err)
		}
		return clip, nil
	}
	return &model.ValidationError{Message: "no target updated"}, nil
}

// ----------------------------------------------------------------------------
// Queries
// ----------------------------------------------------------------------------

// DoGetSigningKey fetches one signing key, tenant-scoped via Commodore.
func (r *Resolver) DoGetSigningKey(ctx context.Context, id string) (*pb.SigningKey, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	sk, err := r.Clients.Commodore.GetSigningKey(ctx, id)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get signing key: %w", err)
	}
	return sk, nil
}

// DoListSigningKeys paginates the tenant's signing keys.
func (r *Resolver) DoListSigningKeys(ctx context.Context, statusFilter *string, page *model.ConnectionInput) (*model.SigningKeysConnection, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	limit := pagination.DefaultLimit
	after := ""
	if page != nil {
		if page.First != nil {
			limit = pagination.ClampLimit(*page.First)
		}
		if page.After != nil {
			after = *page.After
		}
	}
	sf := ""
	if statusFilter != nil {
		sf = *statusFilter
	}
	resp, err := r.Clients.Commodore.ListSigningKeys(ctx, sf, int32(limit), after)
	if err != nil {
		return nil, fmt.Errorf("list signing keys: %w", err)
	}

	edges := make([]*model.SigningKeyEdge, 0, len(resp.GetSigningKeys()))
	nodes := make([]*pb.SigningKey, 0, len(resp.GetSigningKeys()))
	for _, sk := range resp.GetSigningKeys() {
		nodes = append(nodes, sk)
		edges = append(edges, &model.SigningKeyEdge{
			Cursor: sk.GetId(),
			Node:   sk,
		})
	}
	hasNext := resp.GetNextAfterId() != ""
	endCursor := resp.GetNextAfterId()
	return &model.SigningKeysConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   &model.PageInfo{HasNextPage: hasNext, EndCursor: &endCursor},
		TotalCount: len(nodes),
	}, nil
}

// ----------------------------------------------------------------------------
// Field resolvers — playbackPolicy on Stream / VodAsset / Clip
// ----------------------------------------------------------------------------

// DoGetPlaybackPolicyByPlaybackID uses the public policy read path. Webhook
// secrets are omitted by Commodore and never enter the Gateway process.
func (r *Resolver) DoGetPlaybackPolicyByPlaybackID(ctx context.Context, playbackID string) (*model.PlaybackPolicy, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	playbackID = strings.TrimSpace(playbackID)
	if playbackID == "" {
		return nil, nil
	}
	resp, err := r.Clients.Commodore.ResolvePlaybackPolicy(ctx, playbackID)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve playback policy: %w", err)
	}
	return policyToModel(resp), nil
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

type playbackPolicyTarget struct {
	kind string // "stream" | "vod_asset" | "clip"
	id   string
}

// pickPlaybackPolicyTarget enforces the "exactly one of" rule from the schema.
// Returns either a target or a ValidationError result for the caller to return.
func pickPlaybackPolicyTarget(input model.SetPlaybackPolicyInput) (playbackPolicyTarget, *model.ValidationError) {
	count := 0
	var t playbackPolicyTarget
	if input.StreamID != nil && strings.TrimSpace(*input.StreamID) != "" {
		id, err := normalizeStreamID(*input.StreamID)
		if err != nil {
			return t, &model.ValidationError{Message: err.Error()}
		}
		t = playbackPolicyTarget{kind: "stream", id: id}
		count++
	}
	if input.VodAssetID != nil && strings.TrimSpace(*input.VodAssetID) != "" {
		id, err := normalizeVodHash(*input.VodAssetID)
		if err != nil {
			return t, &model.ValidationError{Message: err.Error()}
		}
		t = playbackPolicyTarget{kind: "vod_asset", id: id}
		count++
	}
	if input.ClipID != nil && strings.TrimSpace(*input.ClipID) != "" {
		id, err := normalizeClipHash(*input.ClipID)
		if err != nil {
			return t, &model.ValidationError{Message: err.Error()}
		}
		t = playbackPolicyTarget{kind: "clip", id: id}
		count++
	}
	switch count {
	case 0:
		return t, &model.ValidationError{Message: "exactly one of streamId, vodAssetId, clipId is required"}
	case 1:
		return t, nil
	default:
		return t, &model.ValidationError{Message: "only one of streamId, vodAssetId, clipId may be set"}
	}
}

func protoPolicyType(t model.PlaybackPolicyType) (string, error) {
	switch t {
	case model.PlaybackPolicyTypePublic:
		return "public", nil
	case model.PlaybackPolicyTypeJwt:
		return "jwt", nil
	case model.PlaybackPolicyTypeWebhook:
		return "webhook", nil
	}
	return "", errors.New("invalid policy type")
}

func claimReqsToProto(in []*model.PlaybackJwtClaimRequirementInput) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for _, r := range in {
		if r == nil || r.Name == "" {
			continue
		}
		out[r.Name] = r.JSONValue
	}
	return out
}

// policyToModel converts a Commodore policy response into the GraphQL model
// shape. The Jwt and Webhook fields are autobound to their proto types; the
// secretMasked / requiredClaimsJson field resolvers handle the
// proto→GraphQL conversion (including REDACTING the webhook secret) at
// render time so plaintext never reaches the response payload.
func policyToModel(resp *pb.ResolvePlaybackPolicyResponse) *model.PlaybackPolicy {
	if resp == nil {
		return nil
	}
	t, ok := modelPolicyType(resp.GetType())
	if !ok {
		return nil
	}
	return &model.PlaybackPolicy{
		Type:    t,
		Jwt:     resp.GetJwtPolicy(),     // *proto.PlaybackJwtPolicy
		Webhook: resp.GetWebhookPolicy(), // *proto.PlaybackWebhookPolicy (secret_pt scrubbed by SecretMasked resolver)
	}
}

func modelPolicyType(s string) (model.PlaybackPolicyType, bool) {
	switch strings.ToLower(s) {
	case "public":
		return model.PlaybackPolicyTypePublic, true
	case "jwt":
		return model.PlaybackPolicyTypeJwt, true
	case "webhook":
		return model.PlaybackPolicyTypeWebhook, true
	}
	return "", false
}

// ClaimReqsFromProto converts a proto map<string,string> claim-requirement set
// into the GraphQL list-of-pairs shape. Used by the playbackJwtPolicyResolver
// to render the requiredClaimsJson field.
func ClaimReqsFromProto(in map[string]string) []*model.PlaybackJwtClaimRequirement {
	if len(in) == 0 {
		return nil
	}
	out := make([]*model.PlaybackJwtClaimRequirement, 0, len(in))
	for k, v := range in {
		out = append(out, &model.PlaybackJwtClaimRequirement{Name: k, JSONValue: v})
	}
	return out
}

// WebhookSecretMask is the literal value rendered for the secretMasked field
// on PlaybackWebhookPolicy responses. We never expose the plaintext.
func WebhookSecretMask() string { return webhookSecretMask }

// SigningKeyAlgorithm / Status converters for the SigningKey field resolvers.
func ModelSigningKeyAlgorithm(alg string) model.SigningKeyAlgorithm {
	if strings.EqualFold(alg, "ES256") {
		return model.SigningKeyAlgorithmEs256
	}
	return model.SigningKeyAlgorithmEs256
}

func ModelSigningKeyStatus(s string) model.SigningKeyStatus {
	switch strings.ToLower(s) {
	case "revoked":
		return model.SigningKeyStatusRevoked
	default:
		return model.SigningKeyStatusActive
	}
}

// ParseRFC3339OrNil safely converts an RFC3339 string to a *time.Time pointer.
func ParseRFC3339OrNil(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return nil
		}
	}
	return &t
}

// mapCommodoreErr converts a gRPC status to a GraphQL union error model.
// Returns nil if there's no clean mapping; caller should handle as opaque error.
func mapCommodoreErr(err error) any {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return nil
	}
	switch st.Code() {
	case codes.NotFound:
		return &model.NotFoundError{Message: st.Message()}
	case codes.PermissionDenied, codes.Unauthenticated:
		return &model.AuthError{Message: st.Message()}
	case codes.InvalidArgument:
		return &model.ValidationError{Message: st.Message()}
	case codes.ResourceExhausted:
		return &model.RateLimitError{Message: st.Message()}
	}
	return nil
}
