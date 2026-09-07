package clients

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

// DelegatedJWTForRPC mints a fresh audience-bound assertion for each outbound
// RPC. A distinct JTI per attempt lets downstream services reject replay
// without treating a normal multi-RPC Gateway request as a replay.
func DelegatedJWTForRPC(ctx context.Context, audience string, secret []byte) (string, error) {
	if ctxkeys.GetAuthType(ctx) != "api_token" {
		return ctxkeys.GetDelegatedJWT(ctx, audience), nil
	}
	if len(secret) == 0 {
		assertion := ctxkeys.GetDelegatedJWT(ctx, audience)
		if assertion == "" {
			return "", fmt.Errorf("API-token delegation for %s is not configured", audience)
		}
		return assertion, nil
	}
	tokenID := ctxkeys.GetAPITokenID(ctx)
	if tokenID == "" {
		return "", fmt.Errorf("validated API token ID is missing")
	}
	return auth.GenerateDelegatedAPITokenJWT(
		ctxkeys.GetUserID(ctx),
		ctxkeys.GetTenantID(ctx),
		ctxkeys.GetEmail(ctx),
		ctxkeys.GetRole(ctx),
		tokenID,
		ctxkeys.GetPermissions(ctx),
		audience,
		secret,
	)
}
