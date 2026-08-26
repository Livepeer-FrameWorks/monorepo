package auth

import (
	"errors"
	"testing"
	"time"
)

func TestClusterAccessMaterializationProof(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	proof, mintErr := MintClusterAccessMaterializationProof("secret", "tenant-1", "cluster-1", 4, "stripe:sub-1", "active", now)
	if mintErr != nil {
		t.Fatalf("mint proof: %v", mintErr)
	}
	if verifyErr := VerifyClusterAccessMaterializationProof("secret", proof, "tenant-1", "cluster-1", 4, "stripe:sub-1", "active", now, now.Add(30*time.Second)); verifyErr != nil {
		t.Fatalf("verify proof: %v", verifyErr)
	}

	for name, mutate := range map[string]func() (string, string, int32, string){
		"tenant":    func() (string, string, int32, string) { return "tenant-2", "cluster-1", 4, "stripe:sub-1" },
		"cluster":   func() (string, string, int32, string) { return "tenant-1", "cluster-2", 4, "stripe:sub-1" },
		"source":    func() (string, string, int32, string) { return "tenant-1", "cluster-1", 2, "stripe:sub-1" },
		"reference": func() (string, string, int32, string) { return "tenant-1", "cluster-1", 4, "stripe:sub-2" },
	} {
		t.Run(name, func(t *testing.T) {
			tenantID, clusterID, source, reference := mutate()
			if verifyErr := VerifyClusterAccessMaterializationProof("secret", proof, tenantID, clusterID, source, reference, "active", now, now); !errors.Is(verifyErr, ErrClusterAccessProofInvalid) {
				t.Fatalf("error = %v, want invalid proof", verifyErr)
			}
		})
	}
	if verifyErr := VerifyClusterAccessMaterializationProof("secret", proof, "tenant-1", "cluster-1", 4, "stripe:sub-1", "active", now, now.Add(ClusterAccessProofMaxAge+time.Second)); !errors.Is(verifyErr, ErrClusterAccessProofExpired) {
		t.Fatalf("error = %v, want expired proof", verifyErr)
	}
	if verifyErr := VerifyClusterAccessMaterializationProof("secret", proof, "tenant-1", "cluster-1", 4, "stripe:sub-1", "pending_approval", now, now); !errors.Is(verifyErr, ErrClusterAccessProofInvalid) {
		t.Fatalf("active proof accepted for pending approval: %v", verifyErr)
	}
	if verifyErr := VerifyClusterAccessRevocationProof("secret", proof, "tenant-1", "cluster-1", 4, "stripe:sub-1", now, now); !errors.Is(verifyErr, ErrClusterAccessProofInvalid) {
		t.Fatalf("materialization proof accepted for revocation: %v", verifyErr)
	}
	revokeProof, revokeMintErr := MintClusterAccessRevocationProof("secret", "tenant-1", "cluster-1", 4, "stripe:sub-1", now)
	if revokeMintErr != nil {
		t.Fatalf("mint revocation proof: %v", revokeMintErr)
	}
	if verifyErr := VerifyClusterAccessRevocationProof("secret", revokeProof, "tenant-1", "cluster-1", 4, "stripe:sub-1", now, now); verifyErr != nil {
		t.Fatalf("verify revocation proof: %v", verifyErr)
	}
}
