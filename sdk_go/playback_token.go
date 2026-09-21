package frameworks

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// PlaybackTokenOptions describes a viewer playback token for streams, VOD
// assets, and clips with a JWT playback policy. The token carries the claims
// the gateway's playback verifier checks: kid in the header (one of the
// policy's allowed kids), exp (required), and optionally nbf, aud (matched
// against requiredAudience), and custom claims (matched exactly against
// requiredClaimsJson).
type PlaybackTokenOptions struct {
	// PrivateKeyPEM is the PKCS#8 PEM private key createSigningKey returned.
	PrivateKeyPEM string
	// Kid is the signing key's kid.
	Kid string
	// ExpiresAt is the expiry. Set it or ExpiresIn.
	ExpiresAt time.Time
	// ExpiresIn is the lifetime from IssuedAt.
	ExpiresIn time.Duration
	// IssuedAt defaults to now.
	IssuedAt time.Time
	// NotBefore is the earliest use; zero omits nbf.
	NotBefore time.Time
	// Subject is your viewer's identifier; empty omits sub.
	Subject string
	// Audience is sent as a string when it has one value, as an array when
	// it has several.
	Audience []string
	// Claims are custom claims. They may not set exp, iat, nbf, aud, or sub.
	Claims map[string]any
}

var registeredClaims = []string{"exp", "iat", "nbf", "aud", "sub"}

// canonicalJSON encodes v with object keys sorted at every level and no HTML
// escaping, so every SDK signs the same bytes.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// PlaybackTokenSigningInput returns the header and payload segments of a
// playback token, before signing.
func PlaybackTokenSigningInput(opts PlaybackTokenOptions) (string, error) {
	if opts.Kid == "" {
		return "", errors.New("frameworks: Kid is required")
	}
	issuedAt := opts.IssuedAt
	if issuedAt.IsZero() {
		issuedAt = time.Now()
	}
	var exp time.Time
	switch {
	case !opts.ExpiresAt.IsZero():
		exp = opts.ExpiresAt
	case opts.ExpiresIn > 0:
		exp = issuedAt.Add(opts.ExpiresIn)
	default:
		return "", errors.New("frameworks: ExpiresAt or ExpiresIn is required: the playback verifier rejects tokens without exp")
	}
	payload := map[string]any{}
	for name, value := range opts.Claims {
		for _, reserved := range registeredClaims {
			if name == reserved {
				return "", fmt.Errorf("frameworks: Claims may not set %s; use the matching option", name)
			}
		}
		payload[name] = value
	}
	payload["exp"] = exp.Unix()
	payload["iat"] = issuedAt.Unix()
	if !opts.NotBefore.IsZero() {
		payload["nbf"] = opts.NotBefore.Unix()
	}
	if opts.Subject != "" {
		payload["sub"] = opts.Subject
	}
	switch len(opts.Audience) {
	case 0:
	case 1:
		payload["aud"] = opts.Audience[0]
	default:
		payload["aud"] = opts.Audience
	}
	header, err := canonicalJSON(map[string]any{"alg": "ES256", "kid": opts.Kid, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	body, err := canonicalJSON(payload)
	if err != nil {
		return "", fmt.Errorf("frameworks: encoding claims: %w", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(header) + "." + enc.EncodeToString(body), nil
}

// SignPlaybackToken signs a viewer playback token with a tenant signing key
// (ES256).
func SignPlaybackToken(opts PlaybackTokenOptions) (string, error) {
	signingInput, err := PlaybackTokenSigningInput(opts)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode([]byte(opts.PrivateKeyPEM))
	if block == nil || block.Type != "PRIVATE KEY" {
		return "", errors.New("frameworks: PrivateKeyPEM must be a PKCS#8 PEM (BEGIN PRIVATE KEY), as createSigningKey returns it")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("frameworks: parsing PrivateKeyPEM: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return "", errors.New("frameworks: PrivateKeyPEM must be a P-256 ECDSA key")
	}
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("frameworks: signing: %w", err)
	}
	// JWS carries the raw 32-byte r and s, not ASN.1.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
