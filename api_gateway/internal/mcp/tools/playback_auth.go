package tools

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterPlaybackAuthTools registers playback policy and signing-key
// management tools for agent workflows.
func RegisterPlaybackAuthTools(server *mcp.Server, serviceClients *clients.ServiceClients, _ *resolvers.Resolver, logger logging.Logger) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_signing_keys",
			Description: "List the tenant's playback signing keys. Filter by status='active' or status='revoked'; empty returns all.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ListSigningKeysInput) (*mcp.CallToolResult, any, error) {
			return handleListSigningKeys(ctx, args, serviceClients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "create_signing_key",
			Description: "Generate a new ES256 playback signing keypair. The PRIVATE PEM is returned ONCE in the response and never stored or returned again — capture it before discarding the response. " +
				"Up to 10 active keys per tenant. Requires confirm=\"CREATE SIGNING KEY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CreateSigningKeyInput) (*mcp.CallToolResult, any, error) {
			return handleCreateSigningKey(ctx, args, serviceClients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "revoke_signing_key",
			Description: "Mark an active signing key revoked. Tokens minted with this kid are denied after session re-evaluation. Requires confirm=\"REVOKE SIGNING KEY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args RevokeSigningKeyInput) (*mcp.CallToolResult, any, error) {
			return handleRevokeSigningKey(ctx, args, serviceClients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "set_playback_policy",
			Description: "Set or update the playback access policy on a stream, VOD asset, or clip. " +
				"Exactly one of stream_id / vod_asset_id / clip_id must be provided. " +
				"For JWT policy: provide allowed_kids and optionally required_audience / required_claims. " +
				"For webhook policy: provide url and webhook_secret (HMAC key). " +
				"Mutating a policy invalidates Foghorn caches and re-fires USER_NEW for affected sessions. Requires confirm=\"SET PLAYBACK POLICY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SetPlaybackPolicyInput) (*mcp.CallToolResult, any, error) {
			return handleSetPlaybackPolicy(ctx, args, serviceClients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "clear_playback_policy",
			Description: "Make a stream / VOD asset / clip publicly playable by clearing its access policy. Foghorn caches drop and active sessions re-evaluate. Requires confirm=\"CLEAR PLAYBACK POLICY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ClearPlaybackPolicyInput) (*mcp.CallToolResult, any, error) {
			return handleClearPlaybackPolicy(ctx, args, serviceClients, logger)
		},
	)
}

// ===== Inputs =====

type ListSigningKeysInput struct {
	Status string `json:"status,omitempty" jsonschema_description:"Filter: 'active' | 'revoked'. Empty returns all."`
	Limit  int32  `json:"limit,omitempty" jsonschema_description:"Page size. Default 50."`
}

type CreateSigningKeyInput struct {
	Name    string `json:"name" jsonschema:"required" jsonschema_description:"Human-readable label for the key (e.g. 'primary-2026', 'staging-rotation')."`
	Confirm string `json:"confirm" jsonschema:"required" jsonschema_description:"Must be exactly 'CREATE SIGNING KEY'."`
}

type RevokeSigningKeyInput struct {
	ID      string `json:"id" jsonschema:"required" jsonschema_description:"Signing key ID (commodore.signing_keys.id, UUID) to revoke."`
	Confirm string `json:"confirm" jsonschema:"required" jsonschema_description:"Must be exactly 'REVOKE SIGNING KEY'."`
}

type SetPlaybackPolicyInput struct {
	StreamID   string `json:"stream_id,omitempty" jsonschema_description:"Stream ID (Stream.id). Provide exactly one of stream_id / vod_asset_id / clip_id."`
	VodAssetID string `json:"vod_asset_id,omitempty"`
	ClipID     string `json:"clip_id,omitempty"`
	Type       string `json:"type" jsonschema:"required" jsonschema_description:"Policy type: 'public' | 'jwt' | 'webhook'."`
	// JWT fields
	AllowedKids      []string          `json:"allowed_kids,omitempty" jsonschema_description:"JWT only: signing-key kids that may mint tokens for this resource. Empty = any active key."`
	RequiredAudience []string          `json:"required_audience,omitempty" jsonschema_description:"JWT only: required aud claim values (token must contain at least one)."`
	RequiredClaims   map[string]string `json:"required_claims,omitempty" jsonschema_description:"JWT only: claim name → JSON-encoded expected value. The token's claim must match exactly."`
	// Webhook fields
	WebhookURL       string `json:"webhook_url,omitempty" jsonschema_description:"Webhook only: HTTPS URL to POST a USER_NEW-style payload to. Public IPs only (SSRF-blocked at create time)."`
	WebhookSecret    string `json:"webhook_secret,omitempty" jsonschema_description:"Webhook only: HMAC secret used to sign payloads. Encrypted at rest, never returned in queries."`
	WebhookTimeoutMs int32  `json:"webhook_timeout_ms,omitempty" jsonschema_description:"Webhook only: outbound POST timeout. Server caps at 10000; default 5000."`
	Confirm          string `json:"confirm" jsonschema:"required" jsonschema_description:"Must be exactly 'SET PLAYBACK POLICY'."`
}

type ClearPlaybackPolicyInput struct {
	StreamID   string `json:"stream_id,omitempty"`
	VodAssetID string `json:"vod_asset_id,omitempty"`
	ClipID     string `json:"clip_id,omitempty"`
	Confirm    string `json:"confirm" jsonschema:"required" jsonschema_description:"Must be exactly 'CLEAR PLAYBACK POLICY'."`
}

// ===== Result shapes =====

type SigningKeyResult struct {
	ID           string `json:"id"`
	Kid          string `json:"kid"`
	Name         string `json:"name"`
	Algorithm    string `json:"algorithm"`
	PublicKeyPEM string `json:"public_key_pem"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at,omitempty"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
	RevokedAt    string `json:"revoked_at,omitempty"`
}

type CreateSigningKeyResult struct {
	SigningKey    SigningKeyResult `json:"signing_key"`
	PrivateKeyPEM string           `json:"private_key_pem"` // shown ONCE
	Warning       string           `json:"warning"`
}

type ListSigningKeysResult struct {
	Keys []SigningKeyResult `json:"keys"`
}

type PlaybackPolicyResult struct {
	StreamID     string `json:"stream_id,omitempty"`
	VodAssetID   string `json:"vod_asset_id,omitempty"`
	ClipID       string `json:"clip_id,omitempty"`
	RequiresAuth bool   `json:"requires_auth"`
}

// ===== Handlers =====

func handleListSigningKeys(ctx context.Context, args ListSigningKeysInput, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 50
	}
	resp, err := c.Commodore.ListSigningKeys(ctx, args.Status, limit, "")
	if err != nil {
		logger.WithError(err).Warn("list_signing_keys failed")
		return toolError(fmt.Sprintf("list signing keys: %v", err))
	}
	out := ListSigningKeysResult{Keys: make([]SigningKeyResult, 0, len(resp.GetSigningKeys()))}
	for _, k := range resp.GetSigningKeys() {
		out.Keys = append(out.Keys, signingKeyToResult(k))
	}
	return toolSuccess(out)
}

func handleCreateSigningKey(ctx context.Context, args CreateSigningKeyInput, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requireConfirmation(args.Confirm, "CREATE SIGNING KEY"); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if strings.TrimSpace(args.Name) == "" {
		return toolError("name is required")
	}
	resp, err := c.Commodore.CreateSigningKey(ctx, args.Name)
	if err != nil {
		logger.WithError(err).Warn("create_signing_key failed")
		return toolError(fmt.Sprintf("create signing key: %v", err))
	}
	return toolSuccess(CreateSigningKeyResult{
		SigningKey:    signingKeyToResult(resp.GetSigningKey()),
		PrivateKeyPEM: resp.GetPrivateKeyPem(),
		Warning:       "PRIVATE KEY IS RETURNED ONCE. Capture it now; FrameWorks does not store it and cannot return it again. Lost keys must be revoked and replaced.",
	})
}

func handleRevokeSigningKey(ctx context.Context, args RevokeSigningKeyInput, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requireConfirmation(args.Confirm, "REVOKE SIGNING KEY"); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if args.ID == "" {
		return toolError("id is required")
	}
	resp, err := c.Commodore.RevokeSigningKey(ctx, args.ID)
	if err != nil {
		logger.WithError(err).Warn("revoke_signing_key failed")
		return toolError(fmt.Sprintf("revoke signing key: %v", err))
	}
	return toolSuccess(signingKeyToResult(resp))
}

func handleSetPlaybackPolicy(ctx context.Context, args SetPlaybackPolicyInput, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requireConfirmation(args.Confirm, "SET PLAYBACK POLICY"); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if vErr := validatePlaybackTarget(args.StreamID, args.VodAssetID, args.ClipID); vErr != nil {
		return toolError(vErr.Error())
	}
	policyType := strings.ToLower(strings.TrimSpace(args.Type))
	switch policyType {
	case "public", "jwt", "webhook":
	default:
		return toolError(`type must be "public", "jwt", or "webhook"`)
	}

	req := &pb.SetPlaybackPolicyRequest{
		StreamId:   args.StreamID,
		VodAssetId: args.VodAssetID,
		ClipId:     args.ClipID,
		Type:       policyType,
	}
	if policyType == "jwt" {
		req.Jwt = &pb.PlaybackJwtPolicy{
			AllowedKids:        args.AllowedKids,
			RequiredAudience:   args.RequiredAudience,
			RequiredClaimsJson: args.RequiredClaims,
		}
	}
	if policyType == "webhook" {
		if args.WebhookURL == "" {
			return toolError("webhook_url is required for type=webhook")
		}
		req.Webhook = &pb.PlaybackWebhookPolicy{
			Url:       args.WebhookURL,
			SecretPt:  args.WebhookSecret,
			TimeoutMs: args.WebhookTimeoutMs,
		}
	}

	resp, err := c.Commodore.SetPlaybackPolicy(ctx, req)
	if err != nil {
		logger.WithError(err).Warn("set_playback_policy failed")
		return toolError(fmt.Sprintf("set playback policy: %v", err))
	}
	return toolSuccess(PlaybackPolicyResult{
		StreamID:     resp.GetStreamId(),
		VodAssetID:   resp.GetVodAssetId(),
		ClipID:       resp.GetClipId(),
		RequiresAuth: resp.GetRequiresAuth(),
	})
}

func handleClearPlaybackPolicy(ctx context.Context, args ClearPlaybackPolicyInput, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requireConfirmation(args.Confirm, "CLEAR PLAYBACK POLICY"); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if vErr := validatePlaybackTarget(args.StreamID, args.VodAssetID, args.ClipID); vErr != nil {
		return toolError(vErr.Error())
	}
	resp, err := c.Commodore.SetPlaybackPolicy(ctx, &pb.SetPlaybackPolicyRequest{
		StreamId:   args.StreamID,
		VodAssetId: args.VodAssetID,
		ClipId:     args.ClipID,
		Type:       "public",
	})
	if err != nil {
		logger.WithError(err).Warn("clear_playback_policy failed")
		return toolError(fmt.Sprintf("clear playback policy: %v", err))
	}
	return toolSuccess(PlaybackPolicyResult{
		StreamID:     resp.GetStreamId(),
		VodAssetID:   resp.GetVodAssetId(),
		ClipID:       resp.GetClipId(),
		RequiresAuth: resp.GetRequiresAuth(),
	})
}

// ===== helpers =====

func validatePlaybackTarget(streamID, vodAssetID, clipID string) error {
	count := 0
	for _, v := range []string{streamID, vodAssetID, clipID} {
		if v != "" {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("exactly one of stream_id, vod_asset_id, or clip_id is required")
	}
	return nil
}

func signingKeyToResult(k *pb.SigningKey) SigningKeyResult {
	if k == nil {
		return SigningKeyResult{}
	}
	return SigningKeyResult{
		ID:           k.GetId(),
		Kid:          k.GetKid(),
		Name:         k.GetName(),
		Algorithm:    k.GetAlgorithm(),
		PublicKeyPEM: k.GetPublicKeyPem(),
		Status:       k.GetStatus(),
		CreatedAt:    k.GetCreatedAt(),
		LastUsedAt:   k.GetLastUsedAt(),
		RevokedAt:    k.GetRevokedAt(),
	}
}
