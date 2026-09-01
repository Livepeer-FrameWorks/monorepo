package control

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

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

func MintTranscodeJobTokenFromEnvironment(claims TranscodeJobClaims) (string, error) {
	return MintTranscodeJobToken(os.Getenv("FOGHORN_BALANCER_CAPABILITY_SECRET"), claims)
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

func StampTranscodeJobConfigFromEnvironment(processesJSON string, claims TranscodeJobClaims, now time.Time) (string, error) {
	return StampTranscodeJobConfig(processesJSON, os.Getenv("FOGHORN_BALANCER_CAPABILITY_SECRET"), claims, now)
}

func VerifyTranscodeJobToken(secret, token string, now time.Time) (TranscodeJobClaims, error) {
	if strings.TrimSpace(secret) == "" || strings.TrimSpace(token) == "" {
		return TranscodeJobClaims{}, ErrTranscodeJobTokenMissing
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return TranscodeJobClaims{}, ErrTranscodeJobTokenInvalid
	}
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(transcodeJobTokenDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return TranscodeJobClaims{}, ErrTranscodeJobTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TranscodeJobClaims{}, ErrTranscodeJobTokenInvalid
	}
	var claims TranscodeJobClaims
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&claims); err != nil {
		return TranscodeJobClaims{}, ErrTranscodeJobTokenInvalid
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return TranscodeJobClaims{}, ErrTranscodeJobTokenInvalid
	}
	canonical := canonicalTranscodeJobClaims(claims)
	if err := validateTranscodeJobClaims(canonical); err != nil {
		return TranscodeJobClaims{}, ErrTranscodeJobTokenInvalid
	}
	if claims.IssuedAt > now.UTC().Add(5*time.Minute).Unix() {
		return TranscodeJobClaims{}, ErrTranscodeJobTokenInvalid
	}
	return canonical, nil
}

func VerifyTranscodeJobTokenFromEnvironment(token string, now time.Time) (TranscodeJobClaims, error) {
	return VerifyTranscodeJobToken(os.Getenv("FOGHORN_BALANCER_CAPABILITY_SECRET"), token, now)
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
	if claims.ManifestID == "" || claims.AttemptOrGeneration == "" || claims.Session == "" || claims.NodeID == "" ||
		claims.ClusterID == "" || claims.TenantID == "" || claims.SpecDigest == "" ||
		claims.IssuedAt <= 0 || len(claims.AllowedGatewayClusterIDs) == 0 {
		return ErrTranscodeJobTokenInvalid
	}
	return nil
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
