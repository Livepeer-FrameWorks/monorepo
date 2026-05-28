package auth

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ArtifactRelayPurpose is the fixed `purpose` claim on a JWT minted to
// authorize one cross-cluster (or same-cluster) HTTP GET against an
// origin node's Helmsman /internal/artifact/* route. Carried so the
// same JWT secret cannot be reused for any other token category if
// keys are ever shared.
const ArtifactRelayPurpose = "artifact_relay"

// DefaultArtifactRelayTTL is the lifetime of a freshly-minted peer
// relay token. Short by design — the block-cache fetcher uses it
// once per upstream Range GET and Foghorn re-mints on every resolve.
const DefaultArtifactRelayTTL = 5 * time.Minute

// ArtifactRelayClockSkew is the not-before backdate applied at mint
// time to tolerate small clock drift between origin Foghorn and
// origin Helmsman.
const ArtifactRelayClockSkew = 30 * time.Second

// ArtifactRelayClaims is the JWT claim shape for a peer-relay
// authorization. The binding is fourfold:
//
//   - Audience    = origin node id (Helmsman validates aud == own node)
//   - ArtifactHash = exact artifact (Helmsman validates against URL hash)
//   - Path        = full /internal/artifact/<kind>/<hash>.<ext> path
//     (Helmsman validates request path matches)
//   - Purpose     = ArtifactRelayPurpose
//
// Issuer/Subject are audit metadata (origin cluster / requesting
// cluster) and are not enforced by the validator.
type ArtifactRelayClaims struct {
	Purpose      string `json:"purpose"`
	ArtifactHash string `json:"artifact_hash"`
	Path         string `json:"path"`
	jwt.RegisteredClaims
}

var (
	ErrInvalidArtifactRelay      = errors.New("invalid artifact relay token")
	ErrExpiredArtifactRelay      = errors.New("artifact relay token expired")
	ErrWrongArtifactRelayNode    = errors.New("artifact relay token is for a different node")
	ErrWrongArtifactRelayPurpose = errors.New("token has wrong purpose for artifact relay")
	ErrWrongArtifactRelayHash    = errors.New("artifact relay token is for a different artifact")
	ErrWrongArtifactRelayPath    = errors.New("artifact relay token is for a different path")
)

// GenerateArtifactRelayJWT mints a short-lived token authorizing one
// HTTP GET against an origin node's /internal/artifact/* route.
//
//   - originNodeID       — the specific node that holds the bytes;
//     becomes the audience claim. Helmsman rejects
//     the token unless its own node id matches.
//   - artifactHash       — exact artifact; rejected on mismatch.
//   - requestPath        — full HTTP path the requester will GET
//     (e.g. "/internal/artifact/vod/<hash>.mp4");
//     rejected on mismatch.
//   - originClusterID    — issuer (audit only).
//   - requestingClusterID — subject (audit only).
//
// ttl=0 falls back to DefaultArtifactRelayTTL.
func GenerateArtifactRelayJWT(originNodeID, artifactHash, requestPath, originClusterID, requestingClusterID string, ttl time.Duration, secret []byte) (string, time.Time, error) {
	if originNodeID == "" {
		return "", time.Time{}, fmt.Errorf("origin node id is required")
	}
	if artifactHash == "" {
		return "", time.Time{}, fmt.Errorf("artifact hash is required")
	}
	if requestPath == "" {
		return "", time.Time{}, fmt.Errorf("request path is required")
	}
	if len(secret) == 0 {
		return "", time.Time{}, fmt.Errorf("jwt secret is required")
	}
	if ttl <= 0 {
		ttl = DefaultArtifactRelayTTL
	}
	jti, err := randomJTI()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("jti: %w", err)
	}
	now := time.Now()
	exp := now.Add(ttl)
	claims := &ArtifactRelayClaims{
		Purpose:      ArtifactRelayPurpose,
		ArtifactHash: artifactHash,
		Path:         requestPath,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Issuer:    originClusterID,
			Subject:   requestingClusterID,
			Audience:  jwt.ClaimStrings{originNodeID},
			ExpiresAt: jwt.NewNumericDate(exp),
			NotBefore: jwt.NewNumericDate(now.Add(-ArtifactRelayClockSkew)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// ValidateArtifactRelayJWT verifies signature + expiry + purpose,
// then enforces the (audience == expectedNodeID), artifact hash, and
// path bindings.
//
// All three expected values are required — empty inputs are rejected
// as caller bugs (no "match-any" behavior).
func ValidateArtifactRelayJWT(tokenString string, secret []byte, expectedNodeID, expectedArtifactHash, expectedPath string) (*ArtifactRelayClaims, error) {
	if expectedNodeID == "" {
		return nil, ErrWrongArtifactRelayNode
	}
	if expectedArtifactHash == "" {
		return nil, ErrWrongArtifactRelayHash
	}
	if expectedPath == "" {
		return nil, ErrWrongArtifactRelayPath
	}
	parsed, err := jwt.ParseWithClaims(tokenString, &ArtifactRelayClaims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredArtifactRelay
		}
		return nil, ErrInvalidArtifactRelay
	}
	claims, ok := parsed.Claims.(*ArtifactRelayClaims)
	if !ok || !parsed.Valid {
		return nil, ErrInvalidArtifactRelay
	}
	if claims.Purpose != ArtifactRelayPurpose {
		return nil, ErrWrongArtifactRelayPurpose
	}
	if !audienceMatches(claims.Audience, expectedNodeID) {
		return nil, ErrWrongArtifactRelayNode
	}
	if claims.ArtifactHash != expectedArtifactHash {
		return nil, ErrWrongArtifactRelayHash
	}
	if claims.Path != expectedPath {
		return nil, ErrWrongArtifactRelayPath
	}
	return claims, nil
}

func audienceMatches(aud jwt.ClaimStrings, expected string) bool {
	return slices.Contains(aud, expected)
}
