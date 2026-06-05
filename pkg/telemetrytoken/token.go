// Package telemetrytoken mints and verifies short-lived signed tokens that bind
// a player boot beacon to the serving endpoint resolved for it.
//
// It exists so the cluster-ops analytics surface can trust which node/cluster
// actually served a viewer: a later, unauthenticated beacon cannot prove that on
// its own, so resolveViewerEndpoint stamps a token at resolve time and the player
// echoes it back. This is INFRASTRUCTURE ATTRIBUTION, not playback authorization —
// it is signed with a dedicated platform telemetry secret, never a customer
// playback-auth/JWT key, and carries no viewer identity.
package telemetrytoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const prefix = "v1"

// Claims are the signed, server-derived facts about the serving endpoint. The
// content id ties the token to a specific beacon; tenant/stream/artifact are
// intentionally absent — Bridge re-derives those authoritatively from Commodore.
type Claims struct {
	ContentID        string `json:"cid"`
	NodeID           string `json:"nid"`
	ServingClusterID string `json:"scid"`
	OriginClusterID  string `json:"ocid,omitempty"`
	ExpUnix          int64  `json:"exp"`
}

var (
	ErrEmptySecret  = errors.New("telemetrytoken: empty secret")
	ErrMalformed    = errors.New("telemetrytoken: malformed token")
	ErrBadSignature = errors.New("telemetrytoken: bad signature")
	ErrExpired      = errors.New("telemetrytoken: expired")
)

// Sign returns a compact `v1.<payload>.<sig>` token. ExpUnix is set from ttl when
// not already populated.
func Sign(secret []byte, claims Claims, ttl time.Duration, now time.Time) (string, error) {
	if len(secret) == 0 {
		return "", ErrEmptySecret
	}
	if claims.ExpUnix == 0 {
		claims.ExpUnix = now.Add(ttl).Unix()
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig := sign(secret, encPayload)
	return prefix + "." + encPayload + "." + sig, nil
}

// Verify checks the signature and expiry and returns the decoded claims.
func Verify(secret []byte, token string, now time.Time) (Claims, error) {
	if len(secret) == 0 {
		return Claims{}, ErrEmptySecret
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != prefix {
		return Claims{}, ErrMalformed
	}
	encPayload, gotSig := parts[1], parts[2]
	wantSig := sign(secret, encPayload)
	if !hmac.Equal([]byte(gotSig), []byte(wantSig)) {
		return Claims{}, ErrBadSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var claims Claims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return Claims{}, ErrMalformed
	}
	if claims.ExpUnix > 0 && now.Unix() > claims.ExpUnix {
		return Claims{}, ErrExpired
	}
	return claims, nil
}

func sign(secret []byte, encPayload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
