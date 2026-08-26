package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

const ClusterAccessProofMaxAge = time.Minute

const (
	clusterAccessMaterializeDomain = "cluster-access-materialization-v1"
	clusterAccessRevokeDomain      = "cluster-access-revocation-v1"
)

var (
	ErrClusterAccessProofMissing = errors.New("cluster access materialization proof is missing")
	ErrClusterAccessProofExpired = errors.New("cluster access materialization proof is expired")
	ErrClusterAccessProofInvalid = errors.New("cluster access materialization proof is invalid")
)

// MintClusterAccessMaterializationProof binds a short-lived authorization to
// one exact Purser-approved access transition. The source is the numeric proto
// enum value so the proof remains independent of generated protobuf packages.
func MintClusterAccessMaterializationProof(secret, tenantID, clusterID string, source int32, reference, subscriptionStatus string, authorizedAt time.Time) (string, error) {
	return mintClusterAccessProof(clusterAccessMaterializeDomain, secret, tenantID, clusterID, source, reference, subscriptionStatus, authorizedAt)
}

// MintClusterAccessRevocationProof binds a short-lived authorization to one
// exact Purser-observed commercial revocation. Its distinct domain prevents a
// materialization proof from being replayed as a revocation.
func MintClusterAccessRevocationProof(secret, tenantID, clusterID string, source int32, reference string, authorizedAt time.Time) (string, error) {
	return mintClusterAccessProof(clusterAccessRevokeDomain, secret, tenantID, clusterID, source, reference, "revoked", authorizedAt)
}

func mintClusterAccessProof(domain, secret, tenantID, clusterID string, source int32, reference, subscriptionStatus string, authorizedAt time.Time) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", ErrClusterAccessProofMissing
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(clusterAccessProofMessage(domain, tenantID, clusterID, source, reference, subscriptionStatus, authorizedAt))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyClusterAccessMaterializationProof validates exact request binding and a
// bounded issuance window. Replay within the window is harmless because the
// Quartermaster materialization is idempotent for the same access row.
func VerifyClusterAccessMaterializationProof(secret, proof, tenantID, clusterID string, source int32, reference, subscriptionStatus string, authorizedAt, now time.Time) error {
	return verifyClusterAccessProof(clusterAccessMaterializeDomain, secret, proof, tenantID, clusterID, source, reference, subscriptionStatus, authorizedAt, now)
}

func VerifyClusterAccessRevocationProof(secret, proof, tenantID, clusterID string, source int32, reference string, authorizedAt, now time.Time) error {
	return verifyClusterAccessProof(clusterAccessRevokeDomain, secret, proof, tenantID, clusterID, source, reference, "revoked", authorizedAt, now)
}

func verifyClusterAccessProof(domain, secret, proof, tenantID, clusterID string, source int32, reference, subscriptionStatus string, authorizedAt, now time.Time) error {
	if strings.TrimSpace(secret) == "" || strings.TrimSpace(proof) == "" {
		return ErrClusterAccessProofMissing
	}
	authorizedAt = authorizedAt.UTC()
	now = now.UTC()
	if authorizedAt.After(now.Add(5*time.Second)) || now.Sub(authorizedAt) > ClusterAccessProofMaxAge {
		return ErrClusterAccessProofExpired
	}
	want, err := mintClusterAccessProof(domain, secret, tenantID, clusterID, source, reference, subscriptionStatus, authorizedAt)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(want), []byte(proof)) {
		return ErrClusterAccessProofInvalid
	}
	return nil
}

func clusterAccessProofMessage(domain, tenantID, clusterID string, source int32, reference, subscriptionStatus string, authorizedAt time.Time) []byte {
	return []byte(strings.Join([]string{
		domain,
		strings.TrimSpace(tenantID),
		strings.TrimSpace(clusterID),
		strconv.FormatInt(int64(source), 10),
		strings.TrimSpace(reference),
		strings.TrimSpace(subscriptionStatus),
		strconv.FormatInt(authorizedAt.UTC().Unix(), 10),
	}, "\x00"))
}
