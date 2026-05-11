package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Distinct error sentinels so the trigger handler can log a specific deny
// reason for every failure mode. Operators need this granularity.
var (
	ErrTokenNotJWS       = errors.New("token is not a valid JWS")
	ErrMissingKid        = errors.New("JWT header missing kid")
	ErrUnknownKid        = errors.New("kid does not match any active signing key")
	ErrWrongAlgorithm    = errors.New("JWT alg must be ES256")
	ErrSignatureFailed   = errors.New("JWT signature verification failed")
	ErrMissingExpiration = errors.New("JWT exp claim is required")
	ErrTokenExpired      = errors.New("JWT expired")
	ErrTokenNotYetValid  = errors.New("JWT not yet valid (nbf in the future)")
	ErrAudienceMismatch  = errors.New("JWT audience claim does not satisfy required_audience")
	ErrRequiredClaimMiss = errors.New("JWT required-claim missing or mismatched")
	ErrInvalidPublicKey  = errors.New("invalid signing key (not a parseable ES256 PEM)")
)

// SigningKey is one entry in the tenant's active keyset, as supplied to the
// verifier. The verifier never reads from storage directly; the caller passes
// in the keys it considers acceptable for this request.
type SigningKey struct {
	Kid          string
	PublicKeyPEM string
}

// VerifyOptions matches the policy fields the verifier checks. Empty slices
// mean "no constraint."
type VerifyOptions struct {
	// AllowedKids restricts which signing keys are accepted. Empty = any.
	AllowedKids []string
	// RequiredAudience: the JWT's aud claim must contain at least one of these.
	// Empty = no aud check.
	RequiredAudience []string
	// RequiredClaims are exact-match constraints. Each value is the JSON-encoded
	// representation of the expected claim value (so callers can require strings,
	// numbers, booleans, or arrays consistently).
	RequiredClaims map[string]string
	// SkewTolerance applied to exp / iat / nbf checks.
	SkewTolerance time.Duration
}

// VerifyViewerJWT validates a viewer-supplied JWT against the supplied
// signing-key set and policy options. Returns the parsed claims on success.
//
// Algorithm constraint: ES256 only. Any other alg (`none`, HS*, RS*, etc.)
// is a hard deny — protects against alg-confusion attacks where a token
// signed under a weaker alg verifies against the same public key.
//
// Failure modes return typed errors (ErrTokenNotJWS, ErrUnknownKid, etc.)
// so the caller can log a distinct deny reason without parsing strings.
func VerifyViewerJWT(tokenString string, keys []SigningKey, opts VerifyOptions) (jwt.MapClaims, error) {
	if !looksLikeJWS(tokenString) {
		return nil, ErrTokenNotJWS
	}

	allowedKidSet := map[string]bool{}
	for _, k := range opts.AllowedKids {
		allowedKidSet[k] = true
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"ES256"}),
		jwt.WithLeeway(opts.SkewTolerance),
		jwt.WithExpirationRequired(),
	)

	var resolveErr error
	token, err := parser.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if t.Method == nil || t.Method.Alg() != "ES256" {
			return nil, ErrWrongAlgorithm
		}
		kidVal, ok := t.Header["kid"]
		if !ok {
			resolveErr = ErrMissingKid
			return nil, ErrMissingKid
		}
		kid, kidIsString := kidVal.(string)
		if !kidIsString || kid == "" {
			resolveErr = ErrMissingKid
			return nil, ErrMissingKid
		}
		if len(allowedKidSet) > 0 && !allowedKidSet[kid] {
			resolveErr = ErrUnknownKid
			return nil, ErrUnknownKid
		}

		var matched *SigningKey
		for i := range keys {
			if keys[i].Kid == kid {
				matched = &keys[i]
				break
			}
		}
		if matched == nil {
			resolveErr = ErrUnknownKid
			return nil, ErrUnknownKid
		}

		pub, perr := parseECDSAPublicKey(matched.PublicKeyPEM)
		if perr != nil {
			resolveErr = ErrInvalidPublicKey
			return nil, ErrInvalidPublicKey
		}
		return pub, nil
	})

	if err != nil {
		// Prefer the resolveErr we set during keyfunc — it's more specific
		// than the generic "validation failed" wrapper jwt/v5 returns.
		if resolveErr != nil {
			return nil, resolveErr
		}
		switch {
		case errors.Is(err, jwt.ErrTokenRequiredClaimMissing):
			return nil, ErrMissingExpiration
		case errors.Is(err, jwt.ErrTokenExpired):
			return nil, ErrTokenExpired
		case errors.Is(err, jwt.ErrTokenNotValidYet):
			return nil, ErrTokenNotYetValid
		case errors.Is(err, jwt.ErrTokenSignatureInvalid):
			return nil, ErrSignatureFailed
		case errors.Is(err, jwt.ErrTokenMalformed):
			return nil, ErrTokenNotJWS
		}
		return nil, ErrSignatureFailed
	}

	if !token.Valid {
		return nil, ErrSignatureFailed
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrSignatureFailed
	}

	if len(opts.RequiredAudience) > 0 {
		if !audienceSatisfies(claims, opts.RequiredAudience) {
			return nil, ErrAudienceMismatch
		}
	}

	if len(opts.RequiredClaims) > 0 {
		for name, wantJSON := range opts.RequiredClaims {
			gotVal, present := claims[name]
			if !present {
				return nil, fmt.Errorf("%w: %s", ErrRequiredClaimMiss, name)
			}
			gotJSON, jerr := json.Marshal(gotVal)
			if jerr != nil {
				return nil, fmt.Errorf("%w: %s", ErrRequiredClaimMiss, name)
			}
			if string(gotJSON) != wantJSON {
				return nil, fmt.Errorf("%w: %s", ErrRequiredClaimMiss, name)
			}
		}
	}

	return claims, nil
}

// ViewerJWTKid returns the kid header from a structurally valid JWS.
func ViewerJWTKid(tokenString string) (string, error) {
	if !looksLikeJWS(tokenString) {
		return "", ErrTokenNotJWS
	}
	parts := strings.Split(tokenString, ".")
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrTokenNotJWS
	}
	var hdr map[string]any
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		return "", ErrTokenNotJWS
	}
	kid, ok := hdr["kid"].(string)
	if !ok || kid == "" {
		return "", ErrMissingKid
	}
	return kid, nil
}

// ViewerJWTClaimsUnverified base64-decodes the payload segment of a
// structurally valid JWS without verifying the signature, audience, or any
// required claims. ONLY for operator diagnostics (e.g. surfacing the claim
// payload alongside a deny so the operator sees why the verified path
// rejected the token). Never use as a source of truth for auth decisions.
func ViewerJWTClaimsUnverified(tokenString string) (map[string]any, error) {
	if !looksLikeJWS(tokenString) {
		return nil, ErrTokenNotJWS
	}
	parts := strings.Split(tokenString, ".")
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrTokenNotJWS
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, ErrTokenNotJWS
	}
	return claims, nil
}

// looksLikeJWS does a cheap structural check before handing to the parser.
// Three base64url segments separated by dots, with a JSON header in the first
// segment. Catches Mist-minted session tokens (random opaque strings) before
// they reach the signature path.
func looksLikeJWS(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	if slices.Contains(parts, "") {
		return false
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var hdr map[string]any
	if jerr := json.Unmarshal(hdrBytes, &hdr); jerr != nil {
		return false
	}
	if _, ok := hdr["alg"]; !ok {
		return false
	}
	return true
}

func audienceSatisfies(claims jwt.MapClaims, required []string) bool {
	aud, ok := claims["aud"]
	if !ok {
		return false
	}
	tokenAud := map[string]bool{}
	switch v := aud.(type) {
	case string:
		tokenAud[v] = true
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				tokenAud[s] = true
			}
		}
	default:
		return false
	}
	for _, r := range required {
		if tokenAud[r] {
			return true
		}
	}
	return false
}

func parseECDSAPublicKey(pemStr string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("PEM does not contain an ECDSA public key")
	}
	if ecPub.Curve != elliptic.P256() {
		return nil, errors.New("ECDSA key must use P-256 curve for ES256")
	}
	return ecPub, nil
}

// GenerateES256Keypair creates a new P-256 ECDSA keypair and a short kid.
// Returns PEM-encoded private and public keys plus a base32 kid for embedding
// in JWT headers. Caller stores public + kid; private is shown to the customer
// once and never persisted.
func GenerateES256Keypair() (privatePEM, publicPEM, kid string, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", fmt.Errorf("generate ecdsa key: %w", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal private: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal public: %w", err)
	}
	privatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}))
	publicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))

	kidBytes := make([]byte, 10)
	if _, err := rand.Read(kidBytes); err != nil {
		return "", "", "", fmt.Errorf("kid entropy: %w", err)
	}
	kid = strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(kidBytes))
	return privatePEM, publicPEM, kid, nil
}
