package auth

import (
	"errors"
	"testing"
	"time"
)

func TestWalletBearerAndDelegationAreDistinctCredentials(t *testing.T) {
	secret := []byte("wallet-credential-test")
	bearer, err := GenerateWalletBearerJWT("user", "tenant", "wallet@example.com", "owner", "0xwallet", secret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ValidateWalletBearerJWT(bearer, secret)
	if err != nil || claims.SessionID != "" || claims.TokenID != "" || len(claims.Roles) != 0 {
		t.Fatalf("wallet bearer: claims=%+v err=%v", claims, err)
	}
	if _, interactiveErr := ValidateInteractiveJWT(bearer, secret); !errors.Is(interactiveErr, ErrDelegatedJWT) {
		t.Fatalf("wallet bootstrap accepted as interactive session: %v", interactiveErr)
	}
	credentialExpiry := time.Now().Add(20 * time.Second)
	delegated, err := GenerateDelegatedWalletJWT("user", "tenant", "wallet@example.com", "owner", "0xwallet", "commodore", credentialExpiry, secret)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := ValidateJWT(delegated, secret)
	if err != nil || internal.ExpiresAt.After(credentialExpiry) || internal.ID == claims.ID || internal.Audience[0] != "commodore" {
		t.Fatalf("wallet assertion: claims=%+v err=%v", internal, err)
	}
	if _, err := ValidateWalletBearerJWT(delegated, secret); !errors.Is(err, ErrDelegatedJWT) {
		t.Fatalf("internal wallet assertion accepted as ingress: %v", err)
	}
	if _, err := GenerateDelegatedWalletJWT("user", "tenant", "", "owner", "0xwallet", "commodore", time.Now().Add(-time.Second), secret); err == nil {
		t.Fatal("expired wallet credential produced a new assertion")
	}
}
