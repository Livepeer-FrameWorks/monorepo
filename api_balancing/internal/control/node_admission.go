package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/nodeidentity"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type durableNodeAdmission struct {
	canonicalNodeID string
	tenantID        string
	clusterID       string
	publicKey       []byte
}

const nodeAdmissionValidity = 7 * 24 * time.Hour

const nodeAdmissionControlPlaneConcurrency = 64

var consumeNodeIdentityProofFn = consumeNodeIdentityProof

var nodeAdmissionControlPlaneSlots = make(chan struct{}, nodeAdmissionControlPlaneConcurrency)

func acquireNodeAdmissionControlPlaneSlot() (func(), bool) {
	select {
	case nodeAdmissionControlPlaneSlots <- struct{}{}:
		return func() { <-nodeAdmissionControlPlaneSlots }, true
	default:
		return func() {}, false
	}
}

func stableNodeFingerprintDigest(fingerprint *ipcpb.NodeFingerprint, matchSource quartermasterpb.NodeFingerprintMatchSource) ([]byte, error) {
	if fingerprint == nil {
		return nil, errors.New("stable node fingerprint is missing")
	}
	macs := strings.ToLower(strings.TrimSpace(fingerprint.GetMacsSha256()))
	machineID := strings.ToLower(strings.TrimSpace(fingerprint.GetMachineIdSha256()))
	if macs == "" && machineID == "" {
		return nil, errors.New("stable node fingerprint has no machine or MAC digest")
	}
	for label, value := range map[string]string{"MAC": macs, "machine-id": machineID} {
		if value == "" {
			continue
		}
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("%s fingerprint must be a SHA-256 hex digest", label)
		}
	}
	// Persist the exact stable signal Quartermaster authenticated. In
	// particular, a MAC match must not bless an unrecognized machine-id that
	// happened to be present in the same request.
	identity := ""
	switch matchSource {
	case quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID:
		if machineID == "" {
			return nil, errors.New("machine-id match has no machine-id digest")
		}
		identity = "machine-id=" + machineID
	case quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACS:
		if macs == "" {
			return nil, errors.New("MAC match has no MAC digest")
		}
		identity = "macs=" + macs
	case quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_UNSPECIFIED:
		// Enrollment authenticates and records the supplied fingerprint in one
		// operation. Prefer machine-id there, with MAC only as its fallback.
		identity = "machine-id=" + machineID
		if machineID == "" {
			identity = "macs=" + macs
		}
	default:
		return nil, errors.New("fingerprint match source is not durable")
	}
	digest := sha256.Sum256([]byte("frameworks-node-admission-v1\x00" + identity))
	return digest[:], nil
}

func persistDurableNodeAdmission(ctx context.Context, canonicalNodeID, tenantID, clusterID string, register *ipcpb.Register, matchSource quartermasterpb.NodeFingerprintMatchSource) error {
	if db == nil {
		return errors.New("foghorn database is unavailable")
	}
	canonicalNodeID = strings.TrimSpace(canonicalNodeID)
	tenantID = strings.TrimSpace(tenantID)
	clusterID = strings.TrimSpace(clusterID)
	if canonicalNodeID == "" || tenantID == "" || clusterID == "" {
		return errors.New("durable node admission requires canonical node, tenant, and cluster identity")
	}
	if register == nil || len(register.GetNodeIdentityPublicKeyEd25519()) != 32 {
		return errors.New("durable node admission requires an Ed25519 public key")
	}
	digest, err := stableNodeFingerprintDigest(register.GetFingerprint(), matchSource)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin node admission replacement: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // best effort after commit/error
	queries := foghorndb.New(tx)
	if _, deleteErr := queries.DeleteConflictingNodeAdmissions(ctx, foghorndb.DeleteConflictingNodeAdmissionsParams{
		CanonicalNodeID: canonicalNodeID, FingerprintSha256: digest,
		PublicKeyEd25519: register.GetNodeIdentityPublicKeyEd25519(),
	}); deleteErr != nil {
		return fmt.Errorf("remove superseded node admission: %w", deleteErr)
	}
	stored, err := queries.UpsertNodeAdmission(ctx, foghorndb.UpsertNodeAdmissionParams{
		CanonicalNodeID:   canonicalNodeID,
		FingerprintSha256: digest,
		PublicKeyEd25519:  register.GetNodeIdentityPublicKeyEd25519(),
		TenantID:          tenantID,
		ClusterID:         clusterID,
		ValidUntil:        time.Now().UTC().Add(nodeAdmissionValidity),
	})
	if err != nil {
		return fmt.Errorf("persist node admission: %w", err)
	}
	if stored != canonicalNodeID {
		return errors.New("persisted node admission identity mismatch")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit node admission replacement: %w", err)
	}
	return nil
}

func loadDurableNodeAdmission(ctx context.Context, register *ipcpb.Register) (*durableNodeAdmission, error) {
	if db == nil {
		return nil, errors.New("foghorn database is unavailable")
	}
	if register == nil || len(register.GetNodeIdentityPublicKeyEd25519()) != 32 {
		return nil, errors.New("node identity public key is missing")
	}
	queries := foghorndb.New(db)
	for _, source := range []quartermasterpb.NodeFingerprintMatchSource{
		quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID,
		quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACS,
	} {
		digest, digestErr := stableNodeFingerprintDigest(register.GetFingerprint(), source)
		if digestErr != nil {
			continue
		}
		row, queryErr := queries.GetNodeAdmissionByFingerprint(ctx, digest)
		if errors.Is(queryErr, sql.ErrNoRows) {
			continue
		}
		if queryErr != nil {
			return nil, fmt.Errorf("load node admission: %w", queryErr)
		}
		admission := &durableNodeAdmission{
			canonicalNodeID: strings.TrimSpace(row.CanonicalNodeID),
			tenantID:        strings.TrimSpace(row.TenantID),
			clusterID:       strings.TrimSpace(row.ClusterID),
			publicKey:       append([]byte(nil), row.PublicKeyEd25519...),
		}
		if admission.canonicalNodeID == "" || admission.tenantID == "" || admission.clusterID == "" {
			return nil, errors.New("durable node admission is incomplete")
		}
		if !bytes.Equal(admission.publicKey, register.GetNodeIdentityPublicKeyEd25519()) {
			return nil, errors.New("durable node admission public key mismatch")
		}
		return admission, nil
	}
	return nil, errors.New("stable node fingerprint has no durable admission")
}

func revokeDurableNodeAdmission(ctx context.Context, register *ipcpb.Register) error {
	if db == nil || register == nil || len(register.GetNodeIdentityPublicKeyEd25519()) != 32 {
		return nil
	}
	queries := foghorndb.New(db)
	for _, source := range []quartermasterpb.NodeFingerprintMatchSource{
		quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID,
		quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACS,
	} {
		digest, err := stableNodeFingerprintDigest(register.GetFingerprint(), source)
		if err != nil {
			continue
		}
		if _, err := queries.DeleteNodeAdmissionByFingerprintAndKey(ctx, foghorndb.DeleteNodeAdmissionByFingerprintAndKeyParams{
			FingerprintSha256: digest,
			PublicKeyEd25519:  register.GetNodeIdentityPublicKeyEd25519(),
		}); err != nil {
			return fmt.Errorf("revoke node admission: %w", err)
		}
	}
	return nil
}

func consumeNodeIdentityProof(ctx context.Context, register *ipcpb.Register) error {
	if db == nil {
		return errors.New("foghorn database is unavailable")
	}
	if err := nodeidentity.VerifyRegistration(register, time.Now()); err != nil {
		return err
	}
	publicKeyDigest := sha256.Sum256(register.GetNodeIdentityPublicKeyEd25519())
	issuedAt := register.GetNodeIdentityProofIssuedAt().AsTime().UTC()
	queries := foghorndb.New(db)
	rows, err := queries.ConsumeNodeAdmissionProofNonce(ctx, foghorndb.ConsumeNodeAdmissionProofNonceParams{
		PublicKeySha256: publicKeyDigest[:], Nonce: register.GetNodeIdentityProofNonce(),
		ProofIssuedAt: issuedAt, ExpiresAt: issuedAt.Add(2 * nodeidentity.MaxProofAge),
	})
	if err != nil {
		return fmt.Errorf("consume node identity proof: %w", err)
	}
	if rows != 1 {
		return errors.New("node identity proof nonce was already used")
	}
	if cleanupErr := queries.DeleteExpiredNodeAdmissionProofNonces(ctx); cleanupErr != nil {
		incNodeAdmissionEvent("proof_prune", "failure")
	}
	return nil
}

func nodeIdentityAuthorityUnavailable(err error, resolverConfigured bool) bool {
	if !resolverConfigured {
		return true
	}
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Internal:
		return true
	default:
		return false
	}
}
