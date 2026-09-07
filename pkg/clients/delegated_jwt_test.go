package clients

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func TestDelegatedJWTForRPCMintsFreshAudienceBoundJTI(t *testing.T) {
	secret := []byte("delegation-secret-long-enough")
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, "token-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"billing:write"})
	first, err := DelegatedJWTForRPC(ctx, "purser", secret)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DelegatedJWTForRPC(ctx, "purser", secret)
	if err != nil {
		t.Fatal(err)
	}
	firstClaims, err := auth.ValidateJWT(first, secret)
	if err != nil {
		t.Fatal(err)
	}
	secondClaims, err := auth.ValidateJWT(second, secret)
	if err != nil {
		t.Fatal(err)
	}
	if firstClaims.ID == "" || firstClaims.ID == secondClaims.ID {
		t.Fatalf("JTIs were not fresh: %q %q", firstClaims.ID, secondClaims.ID)
	}
	if firstClaims.TokenID != "token-1" || firstClaims.TenantID != "tenant-1" || len(firstClaims.Audience) != 1 || firstClaims.Audience[0] != "purser" {
		t.Fatalf("unexpected delegated claims: %+v", firstClaims)
	}
}

func TestDelegatedJWTForRPCFailsClosedWithoutAssertionOrSigningKey(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	if token, err := DelegatedJWTForRPC(ctx, "purser", nil); err == nil || token != "" || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing delegation = token %q, err %v; want fail closed", token, err)
	}
}

func TestDelegatedJWTForRPCUsesValidatedPreMintedAssertionWithoutSigningKey(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyDelegatedJWTs, map[string]string{"purser": "delegated-assertion"})
	token, err := DelegatedJWTForRPC(ctx, "purser", nil)
	if err != nil || token != "delegated-assertion" {
		t.Fatalf("pre-minted delegation = token %q, err %v", token, err)
	}
}
