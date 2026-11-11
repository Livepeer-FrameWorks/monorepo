package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// edgeDeployReceipt retains committed progress without enrollment credentials.
type edgeDeployReceipt struct {
	ClusterID             string    `json:"cluster_id"`
	BridgeURL             string    `json:"bridge_url"`
	SSHTarget             string    `json:"ssh_target,omitempty"`
	NodeHint              string    `json:"node_hint,omitempty"`
	NodeID                string    `json:"node_id,omitempty"`
	Phase                 string    `json:"phase"`
	ClusterCreated        bool      `json:"cluster_created"`
	EnrollmentTokenIssued bool      `json:"enrollment_token_issued"`
	Installed             bool      `json:"installed"`
	HTTPSVerified         bool      `json:"https_verified"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func writeEdgeDeployReceipt(dir string, receipt edgeDeployReceipt) (string, error) {
	if receipt.ClusterID == "" {
		return "", fmt.Errorf("deployment receipt requires a cluster ID")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	receipt.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(receipt.BridgeURL + "\x00" + receipt.ClusterID))
	dest := filepath.Join(dir, hex.EncodeToString(sum[:16])+".json")
	temp, err := os.CreateTemp(dir, ".deploy-*")
	if err != nil {
		return "", err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err = temp.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(name, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (cfg *deployConfig) recordDeployPhase(phase string) error {
	if cfg.receipt == nil {
		cfg.receipt = &edgeDeployReceipt{}
	}
	cfg.receipt.ClusterID = cfg.clusterID
	cfg.receipt.SSHTarget = cfg.sshTarget
	cfg.receipt.NodeHint = deriveEdgeNodeName(cfg.nodeName, "", cfg.sshTarget, cfg.sshTarget == "")
	cfg.receipt.Phase = phase
	cfg.receipt.EnrollmentTokenIssued = cfg.enrollmentToken != ""
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	path, err := writeEdgeDeployReceipt(filepath.Join(cacheDir, "frameworks", "edge-deploy"), *cfg.receipt)
	if err != nil {
		return fmt.Errorf("save committed deployment progress for cluster %s: %w", cfg.clusterID, err)
	}
	cfg.receiptPath = path
	return nil
}
