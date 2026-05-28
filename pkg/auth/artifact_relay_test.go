package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testRelayNode     = "edge-us-east-1a-42"
	testRelayArtifact = "abc123def456"
	testRelayPath     = "/internal/artifact/vod/abc123def456.mp4"
	testRelayOrigin   = "media-us-east"
	testRelayPeer     = "media-eu-west"
)

var testRelaySecret = []byte("test-secret-not-for-prod")

func TestArtifactRelayJWT_MintValidateRoundTrip(t *testing.T) {
	token, exp, err := GenerateArtifactRelayJWT(testRelayNode, testRelayArtifact, testRelayPath, testRelayOrigin, testRelayPeer, 0, testRelaySecret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	if time.Until(exp) <= 0 || time.Until(exp) > DefaultArtifactRelayTTL+time.Second {
		t.Fatalf("exp out of range: %v", exp)
	}
	claims, err := ValidateArtifactRelayJWT(token, testRelaySecret, testRelayNode, testRelayArtifact, testRelayPath)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.Purpose != ArtifactRelayPurpose {
		t.Fatalf("purpose=%q want %q", claims.Purpose, ArtifactRelayPurpose)
	}
	if claims.Issuer != testRelayOrigin {
		t.Fatalf("iss=%q want %q", claims.Issuer, testRelayOrigin)
	}
	if claims.Subject != testRelayPeer {
		t.Fatalf("sub=%q want %q", claims.Subject, testRelayPeer)
	}
	if !audienceMatches(claims.Audience, testRelayNode) {
		t.Fatalf("aud missing %s: %v", testRelayNode, claims.Audience)
	}
}

func TestArtifactRelayJWT_WrongNodeRejected(t *testing.T) {
	token, _, err := GenerateArtifactRelayJWT(testRelayNode, testRelayArtifact, testRelayPath, testRelayOrigin, testRelayPeer, 0, testRelaySecret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, err = ValidateArtifactRelayJWT(token, testRelaySecret, "different-node", testRelayArtifact, testRelayPath)
	if !errors.Is(err, ErrWrongArtifactRelayNode) {
		t.Fatalf("err=%v want ErrWrongArtifactRelayNode", err)
	}
}

func TestArtifactRelayJWT_WrongArtifactHashRejected(t *testing.T) {
	token, _, err := GenerateArtifactRelayJWT(testRelayNode, testRelayArtifact, testRelayPath, testRelayOrigin, testRelayPeer, 0, testRelaySecret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, err = ValidateArtifactRelayJWT(token, testRelaySecret, testRelayNode, "different-hash", testRelayPath)
	if !errors.Is(err, ErrWrongArtifactRelayHash) {
		t.Fatalf("err=%v want ErrWrongArtifactRelayHash", err)
	}
}

func TestArtifactRelayJWT_WrongPathRejected(t *testing.T) {
	token, _, err := GenerateArtifactRelayJWT(testRelayNode, testRelayArtifact, testRelayPath, testRelayOrigin, testRelayPeer, 0, testRelaySecret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, err = ValidateArtifactRelayJWT(token, testRelaySecret, testRelayNode, testRelayArtifact, "/internal/artifact/vod/other.mp4")
	if !errors.Is(err, ErrWrongArtifactRelayPath) {
		t.Fatalf("err=%v want ErrWrongArtifactRelayPath", err)
	}
}

func TestArtifactRelayJWT_ExpiredRejected(t *testing.T) {
	// Mint with negative TTL by reaching into the lower-level jwt directly,
	// then validate via the public API.
	claims := &ArtifactRelayClaims{
		Purpose:      ArtifactRelayPurpose,
		ArtifactHash: testRelayArtifact,
		Path:         testRelayPath,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    testRelayOrigin,
			Subject:   testRelayPeer,
			Audience:  jwt.ClaimStrings{testRelayNode},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Minute)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(testRelaySecret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = ValidateArtifactRelayJWT(signed, testRelaySecret, testRelayNode, testRelayArtifact, testRelayPath)
	if !errors.Is(err, ErrExpiredArtifactRelay) {
		t.Fatalf("err=%v want ErrExpiredArtifactRelay", err)
	}
}

func TestArtifactRelayJWT_WrongPurposeRejected(t *testing.T) {
	// A token from a different purpose family signed with the same secret
	// must be rejected. Simulate by minting a claim with a different
	// Purpose value.
	claims := &ArtifactRelayClaims{
		Purpose:      "edge_mist_admin", // wrong purpose
		ArtifactHash: testRelayArtifact,
		Path:         testRelayPath,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{testRelayNode},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(testRelaySecret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = ValidateArtifactRelayJWT(signed, testRelaySecret, testRelayNode, testRelayArtifact, testRelayPath)
	if !errors.Is(err, ErrWrongArtifactRelayPurpose) {
		t.Fatalf("err=%v want ErrWrongArtifactRelayPurpose", err)
	}
}

func TestArtifactRelayJWT_WrongSecretRejected(t *testing.T) {
	token, _, err := GenerateArtifactRelayJWT(testRelayNode, testRelayArtifact, testRelayPath, testRelayOrigin, testRelayPeer, 0, testRelaySecret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, err = ValidateArtifactRelayJWT(token, []byte("other-secret"), testRelayNode, testRelayArtifact, testRelayPath)
	if !errors.Is(err, ErrInvalidArtifactRelay) {
		t.Fatalf("err=%v want ErrInvalidArtifactRelay", err)
	}
}

func TestArtifactRelayJWT_EmptyExpectedRejected(t *testing.T) {
	token, _, err := GenerateArtifactRelayJWT(testRelayNode, testRelayArtifact, testRelayPath, testRelayOrigin, testRelayPeer, 0, testRelaySecret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := ValidateArtifactRelayJWT(token, testRelaySecret, "", testRelayArtifact, testRelayPath); !errors.Is(err, ErrWrongArtifactRelayNode) {
		t.Fatalf("empty node: %v", err)
	}
	if _, err := ValidateArtifactRelayJWT(token, testRelaySecret, testRelayNode, "", testRelayPath); !errors.Is(err, ErrWrongArtifactRelayHash) {
		t.Fatalf("empty hash: %v", err)
	}
	if _, err := ValidateArtifactRelayJWT(token, testRelaySecret, testRelayNode, testRelayArtifact, ""); !errors.Is(err, ErrWrongArtifactRelayPath) {
		t.Fatalf("empty path: %v", err)
	}
}

func TestArtifactRelayJWT_EmptyInputsAtMintRejected(t *testing.T) {
	cases := []struct {
		name                               string
		node, artifact, path, origin, peer string
		secret                             []byte
		expectErrSubstr                    string
	}{
		{"empty node", "", testRelayArtifact, testRelayPath, testRelayOrigin, testRelayPeer, testRelaySecret, "node id"},
		{"empty artifact", testRelayNode, "", testRelayPath, testRelayOrigin, testRelayPeer, testRelaySecret, "artifact hash"},
		{"empty path", testRelayNode, testRelayArtifact, "", testRelayOrigin, testRelayPeer, testRelaySecret, "request path"},
		{"empty secret", testRelayNode, testRelayArtifact, testRelayPath, testRelayOrigin, testRelayPeer, nil, "secret"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := GenerateArtifactRelayJWT(c.node, c.artifact, c.path, c.origin, c.peer, 0, c.secret)
			if err == nil {
				t.Fatalf("want error for %s, got nil", c.name)
			}
		})
	}
}
