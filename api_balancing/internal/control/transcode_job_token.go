package control

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"frameworks/api_balancing/internal/appconfig"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

const transcodeJobTokenDomain = "foghorn-transcode-job-v1"

var (
	ErrTranscodeJobTokenMissing = errors.New("transcode job token is missing")
	ErrTranscodeJobTokenInvalid = errors.New("transcode job token is invalid")
)

// TranscodeJobClaims is the complete authorization envelope carried from
// Foghorn, through Mist, to go-livepeer. Lifetime is checked against the
// authoritative live generation or processing attempt by HandleLivepeerAuth;
// live jobs deliberately do not use a wall-clock expiry.
type TranscodeJobClaims struct {
	ManifestID               string   `json:"manifest_id"`
	JobID                    string   `json:"job_id,omitempty"`
	AttemptOrGeneration      string   `json:"attempt_or_generation"`
	Session                  string   `json:"session,omitempty"`
	NodeID                   string   `json:"node_id"`
	ClusterID                string   `json:"cluster_id"`
	TenantID                 string   `json:"tenant_id"`
	SpecDigest               string   `json:"spec_digest"`
	AllowedGatewayClusterIDs []string `json:"allowed_gateway_cluster_ids"`
	IssuedAt                 int64    `json:"issued_at"`
}

func MintTranscodeJobToken(secret string, claims TranscodeJobClaims) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", ErrTranscodeJobTokenMissing
	}
	claims = canonicalTranscodeJobClaims(claims)
	if err := validateTranscodeJobClaims(claims); err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal transcode job claims: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(transcodeJobTokenDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "v1." + encoded + "." + sig, nil
}

func MintTranscodeJobTokenWithConfiguredSecret(claims TranscodeJobClaims) (string, error) {
	return MintTranscodeJobToken(appconfig.Current().BalancerCapabilitySecret, claims)
}

// StampTranscodeJobConfig attaches a job/generation-bound delivery capability to the
// Livepeer process entries in an otherwise authoritative, token-free process
// config. Callers must persist/cache processesJSON, never the returned value.
func StampTranscodeJobConfig(processesJSON, secret string, claims TranscodeJobClaims, now time.Time) (string, error) {
	if !mist.HasLivepeerProcesses(processesJSON) {
		return processesJSON, nil
	}
	authoritative := mist.StripLivepeerJobToken(processesJSON)
	digest, err := mist.LivepeerJobSpecDigest(authoritative)
	if err != nil {
		return "", err
	}
	allowed := mist.LivepeerGatewayClusters(authoritative)
	if len(allowed) == 0 {
		return "", fmt.Errorf("livepeer process has no authorized gateway cluster")
	}
	claims.SpecDigest = digest
	claims.AllowedGatewayClusterIDs = allowed
	claims.IssuedAt = now.UTC().Unix()
	token, err := MintTranscodeJobToken(secret, claims)
	if err != nil {
		return "", err
	}
	return mist.SetLivepeerJobToken(authoritative, token), nil
}

func StampTranscodeJobConfigWithConfiguredSecret(processesJSON string, claims TranscodeJobClaims, now time.Time) (string, error) {
	return StampTranscodeJobConfig(processesJSON, appconfig.Current().BalancerCapabilitySecret, claims, now)
}

// transcodeJobTokenIssuedAtSkew is how far in the future a token's issued_at may be.
// Tokens are minted by one Foghorn replica and verified by the gateway's cell
// Foghorn, possibly on another host; the check only rejects implausible stamps, so
// it stays tolerant of ordinary clock drift between hosts.
const transcodeJobTokenIssuedAtSkew = 5 * time.Minute

// Verification failure checks, reported as TranscodeJobTokenError.Check and used as
// metric/log reason labels.
const (
	TranscodeTokenCheckMissing      = "missing"
	TranscodeTokenCheckFormat       = "format"
	TranscodeTokenCheckHMAC         = "hmac"
	TranscodeTokenCheckPayload      = "payload"
	TranscodeTokenCheckUnknownField = "unknown_field"
	TranscodeTokenCheckMissingClaim = "missing_claim"
	TranscodeTokenCheckIssuedAtSkew = "iat_skew"
)

// TranscodeJobTokenError names the verification check a token failed. It matches
// ErrTranscodeJobTokenMissing or ErrTranscodeJobTokenInvalid under errors.Is.
type TranscodeJobTokenError struct {
	Check  string
	Detail string
}

func (e *TranscodeJobTokenError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("transcode job token %s check failed: %s", e.Check, e.Detail)
	}
	return fmt.Sprintf("transcode job token %s check failed", e.Check)
}

func (e *TranscodeJobTokenError) Is(target error) bool {
	if e.Check == TranscodeTokenCheckMissing {
		return target == ErrTranscodeJobTokenMissing
	}
	return target == ErrTranscodeJobTokenInvalid
}

// TranscodeJobTokenCheck returns the failed check of a verification error, or ""
// when err is not a token verification error.
func TranscodeJobTokenCheck(err error) string {
	var tokenErr *TranscodeJobTokenError
	if errors.As(err, &tokenErr) {
		return tokenErr.Check
	}
	return ""
}

func tokenCheckFailed(check, detail string) (TranscodeJobClaims, error) {
	return TranscodeJobClaims{}, &TranscodeJobTokenError{Check: check, Detail: detail}
}

func VerifyTranscodeJobToken(secret, token string, now time.Time) (TranscodeJobClaims, error) {
	if strings.TrimSpace(secret) == "" {
		return tokenCheckFailed(TranscodeTokenCheckMissing, "verifier has no capability secret")
	}
	if strings.TrimSpace(token) == "" {
		return tokenCheckFailed(TranscodeTokenCheckMissing, "request carries no job token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return tokenCheckFailed(TranscodeTokenCheckFormat, fmt.Sprintf("%d dot-separated parts", len(parts)))
	}
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(transcodeJobTokenDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return tokenCheckFailed(TranscodeTokenCheckHMAC, "signature does not match this verifier's capability secret")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return tokenCheckFailed(TranscodeTokenCheckPayload, "claims are not base64url")
	}
	var claims TranscodeJobClaims
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&claims); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return tokenCheckFailed(TranscodeTokenCheckUnknownField, err.Error())
		}
		return tokenCheckFailed(TranscodeTokenCheckPayload, err.Error())
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return tokenCheckFailed(TranscodeTokenCheckPayload, "trailing data after claims")
	}
	canonical := canonicalTranscodeJobClaims(claims)
	if missing := missingTranscodeJobClaims(canonical); len(missing) > 0 {
		return tokenCheckFailed(TranscodeTokenCheckMissingClaim, strings.Join(missing, ","))
	}
	if limit := now.UTC().Add(transcodeJobTokenIssuedAtSkew).Unix(); claims.IssuedAt > limit {
		return tokenCheckFailed(TranscodeTokenCheckIssuedAtSkew, fmt.Sprintf("issued_at %d is %ds ahead of the verifier clock", claims.IssuedAt, claims.IssuedAt-now.UTC().Unix()))
	}
	return canonical, nil
}

func VerifyTranscodeJobTokenWithConfiguredSecret(token string, now time.Time) (TranscodeJobClaims, error) {
	return VerifyTranscodeJobToken(appconfig.Current().BalancerCapabilitySecret, token, now)
}

func canonicalTranscodeJobClaims(claims TranscodeJobClaims) TranscodeJobClaims {
	claims.ManifestID = strings.TrimSpace(claims.ManifestID)
	claims.JobID = strings.TrimSpace(claims.JobID)
	claims.AttemptOrGeneration = strings.TrimSpace(claims.AttemptOrGeneration)
	claims.Session = strings.TrimSpace(claims.Session)
	claims.NodeID = strings.TrimSpace(claims.NodeID)
	claims.ClusterID = strings.TrimSpace(claims.ClusterID)
	claims.TenantID = strings.TrimSpace(claims.TenantID)
	claims.SpecDigest = strings.TrimSpace(claims.SpecDigest)
	seen := map[string]struct{}{}
	allowed := make([]string, 0, len(claims.AllowedGatewayClusterIDs))
	for _, id := range claims.AllowedGatewayClusterIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		allowed = append(allowed, id)
	}
	sort.Strings(allowed)
	claims.AllowedGatewayClusterIDs = allowed
	return claims
}

func validateTranscodeJobClaims(claims TranscodeJobClaims) error {
	if len(missingTranscodeJobClaims(claims)) > 0 {
		return ErrTranscodeJobTokenInvalid
	}
	return nil
}

// missingTranscodeJobClaims lists the JSON names of required claims that are empty.
func missingTranscodeJobClaims(claims TranscodeJobClaims) []string {
	var missing []string
	add := func(empty bool, name string) {
		if empty {
			missing = append(missing, name)
		}
	}
	add(claims.ManifestID == "", "manifest_id")
	add(claims.AttemptOrGeneration == "", "attempt_or_generation")
	add(claims.Session == "", "session")
	add(claims.NodeID == "", "node_id")
	add(claims.ClusterID == "", "cluster_id")
	add(claims.TenantID == "", "tenant_id")
	add(claims.SpecDigest == "", "spec_digest")
	add(claims.IssuedAt <= 0, "issued_at")
	add(len(claims.AllowedGatewayClusterIDs) == 0, "allowed_gateway_cluster_ids")
	return missing
}

func TranscodeJobTokenAllowsGatewayCluster(claims TranscodeJobClaims, clusterID string) bool {
	clusterID = strings.TrimSpace(clusterID)
	for _, allowed := range claims.AllowedGatewayClusterIDs {
		if allowed == clusterID {
			return true
		}
	}
	return false
}
