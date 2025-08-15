package auth

import (
	"testing"
)

func TestPasswordHashAndCheck(t *testing.T) {
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatalf("hash error: %v", err)
	}
	if !CheckPassword("secret", hash) {
		t.Fatalf("password should match")
	}
	if CheckPassword("wrong", hash) {
		t.Fatalf("password should not match")
	}
}

func TestValidateServiceToken(t *testing.T) {
	if err := ValidateServiceToken("", "expected"); err == nil {
		t.Fatalf("expected missing token error")
	}
	if err := ValidateServiceToken("bad", "expected"); err == nil {
		t.Fatalf("expected invalid token error")
	}
	if err := ValidateServiceToken("expected", "expected"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJWTGenerateValidate(t *testing.T) {
	secret := []byte("s3cr3t")
	token, err := GenerateJWT("user1", "tenant1", "u@example.com", "admin", secret)
	if err != nil {
		t.Fatalf("generate jwt: %v", err)
	}
	claims, err := ValidateJWT(token, secret)
	if err != nil {
		t.Fatalf("validate jwt: %v", err)
	}
	if claims.UserID != "user1" || claims.TenantID != "tenant1" {
		t.Fatalf("claims mismatch")
	}
}
