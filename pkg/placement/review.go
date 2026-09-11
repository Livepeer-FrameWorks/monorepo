package placement

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"
)

const (
	ReviewLifetime  = 5 * time.Minute
	reviewClockSkew = 5 * time.Second
	reviewDomain    = "frameworks/placement/review/v1\x00"
)

var ErrStaleReview = errors.New("placement review is stale or does not match this change")

// ReviewBinding contains no credentials or private inventory. ContextDigest binds
// owner/entitlement/price facts and the impact shown to the user. Reissuing a review
// for identical intent does not change its durable apply identity.
type ReviewBinding struct {
	TenantID               string
	ActorID                string
	ScopeKind              string
	ScopeID                string
	ExpectedRevision       uint64
	ExpectedParentRevision uint64
	PolicyDigest           string
	ContextDigest          string
	RequiredWarnings       []string
}

type reviewClaims struct {
	SchemaVersion uint32
	KeyID         string
	IssuedAt      int64
	ExpiresAt     int64
	Binding       ReviewBinding
}

func IssueReview(keyID string, key ed25519.PrivateKey, binding ReviewBinding, now time.Time) (string, error) {
	if len(key) != ed25519.PrivateKeySize || !validReviewID(keyID) || now.Unix() <= 0 {
		return "", fmt.Errorf("review signing key, key ID and current time are required")
	}
	canonical, err := canonicalReview(binding)
	if err != nil {
		return "", err
	}
	claims := reviewClaims{SchemaVersion: 1, KeyID: keyID, IssuedAt: now.Unix(), ExpiresAt: now.Add(ReviewLifetime).Unix(), Binding: canonical}
	encoded, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	if len(encoded) > 12000 {
		return "", fmt.Errorf("placement review exceeds transport size limit")
	}
	signature := ed25519.Sign(key, append([]byte(reviewDomain), encoded...))
	return base64.RawURLEncoding.EncodeToString(encoded) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// VerifyReview is fail-closed on expiry, unknown keys/fields, changed intent,
// actor/scope substitution and changed acknowledgement requirements. Callers must
// still check current authorization and recompute ContextDigest before applying.
func VerifyReview(token string, trust map[string]ed25519.PublicKey, expected ReviewBinding, now time.Time) error {
	claims, err := readSignedReview(token, trust)
	if err != nil || now.IsZero() {
		return ErrStaleReview
	}
	if claims.IssuedAt > now.Add(reviewClockSkew).Unix() || now.Unix() >= claims.ExpiresAt {
		return ErrStaleReview
	}
	want, err := ReviewDigest(expected)
	if err != nil {
		return ErrStaleReview
	}
	got, err := ReviewDigest(claims.Binding)
	if err != nil || got != want {
		return ErrStaleReview
	}
	return nil
}

// SignedReviewBinding authenticates the original intent, including an expired
// token, so a committed command can be reconstructed for idempotency comparison.
// It is NOT validation for a new apply: that path must call VerifyReview against
// freshly recomputed facts and revisions inside the write transaction.
func SignedReviewBinding(token string, trust map[string]ed25519.PublicKey) (ReviewBinding, error) {
	claims, err := readSignedReview(token, trust)
	return claims.Binding, err
}

func readSignedReview(token string, trust map[string]ed25519.PublicKey) (reviewClaims, error) {
	if len(token) > 16384 {
		return reviewClaims{}, ErrStaleReview
	}
	body, signaturePart, ok := strings.Cut(token, ".")
	if !ok {
		return reviewClaims{}, ErrStaleReview
	}
	encoded, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(encoded) > 12000 {
		return reviewClaims{}, ErrStaleReview
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return reviewClaims{}, ErrStaleReview
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var claims reviewClaims
	if decodeErr := decoder.Decode(&claims); decodeErr != nil {
		return reviewClaims{}, ErrStaleReview
	}
	if trailingErr := decoder.Decode(new(any)); !errors.Is(trailingErr, io.EOF) {
		return reviewClaims{}, ErrStaleReview
	}
	key := trust[claims.KeyID]
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, append([]byte(reviewDomain), encoded...), signature) {
		return reviewClaims{}, ErrStaleReview
	}
	if claims.SchemaVersion != 1 || claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > int64(ReviewLifetime/time.Second) {
		return reviewClaims{}, ErrStaleReview
	}
	canonical, err := canonicalReview(claims.Binding)
	if err != nil {
		return reviewClaims{}, ErrStaleReview
	}
	claims.Binding = canonical
	return claims, nil
}

func ReviewDigest(binding ReviewBinding) (string, error) {
	canonical, err := canonicalReview(binding)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(reviewDomain+"binding\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

// ValidateReviewAcknowledgements requires an explicit acknowledgement for every
// cost/availability warning that was reviewed, and rejects invented warning IDs.
func ValidateReviewAcknowledgements(binding ReviewBinding, acknowledged []string) error {
	canonical, err := canonicalReview(binding)
	if err != nil {
		return err
	}
	if len(acknowledged) > 64 {
		return fmt.Errorf("too many warning acknowledgements")
	}
	actual := slices.Clone(acknowledged)
	slices.Sort(actual)
	actual = slices.Compact(actual)
	if !slices.Equal(actual, canonical.RequiredWarnings) {
		return fmt.Errorf("acknowledgements do not match the reviewed warnings")
	}
	return nil
}

func canonicalReview(binding ReviewBinding) (ReviewBinding, error) {
	for _, value := range []string{binding.TenantID, binding.ActorID, binding.ScopeID} {
		if !validReviewID(value) {
			return ReviewBinding{}, fmt.Errorf("review identity is required")
		}
	}
	if binding.ScopeKind != "tenant" && binding.ScopeKind != "stream" && binding.ScopeKind != "cluster_consent" {
		return ReviewBinding{}, fmt.Errorf("invalid placement review scope")
	}
	if binding.ScopeKind == "tenant" && binding.ScopeID != binding.TenantID {
		return ReviewBinding{}, fmt.Errorf("tenant review scope mismatch")
	}
	if binding.ExpectedRevision > math.MaxInt64 || binding.ExpectedParentRevision > math.MaxInt64 || (binding.ScopeKind != "stream" && binding.ExpectedParentRevision != 0) {
		return ReviewBinding{}, fmt.Errorf("invalid placement review revision")
	}
	for _, digest := range []string{binding.PolicyDigest, binding.ContextDigest} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != digest {
			return ReviewBinding{}, fmt.Errorf("review requires canonical SHA-256 digests")
		}
	}
	if len(binding.RequiredWarnings) > 64 {
		return ReviewBinding{}, fmt.Errorf("too many placement warnings")
	}
	binding.RequiredWarnings = slices.Clone(binding.RequiredWarnings)
	for _, warning := range binding.RequiredWarnings {
		if !validReviewID(warning) {
			return ReviewBinding{}, fmt.Errorf("invalid placement warning ID")
		}
	}
	slices.Sort(binding.RequiredWarnings)
	binding.RequiredWarnings = slices.Compact(binding.RequiredWarnings)
	if len(binding.RequiredWarnings) == 0 {
		binding.RequiredWarnings = nil
	}
	return binding, nil
}

func validReviewID(value string) bool {
	return value != "" && len(value) <= 255 && strings.TrimSpace(value) == value && strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) < 0
}
