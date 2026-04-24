package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadedStaticPeers is the minimum shape of static-peers.json as
// api_mesh/internal/agent.loadStaticPeers parses it. We mirror it here
// rather than exporting the package-private struct; the schema round-trip
// test asserts we produce exactly those keys.
type loadedStaticPeers struct {
	Version string              `json:"version"`
	Peers   []loadedStaticPeer  `json:"peers"`
	DNS     map[string][]string `json:"dns,omitempty"`
}

type loadedStaticPeer struct {
	Name       string   `json:"name"`
	PublicKey  string   `json:"public_key"`
	AllowedIPs []string `json:"allowed_ips"`
	Endpoint   string   `json:"endpoint,omitempty"`
	KeepAlive  int      `json:"keep_alive,omitempty"`
}

func TestRenderSeedPeers_MatchesAgentSchema(t *testing.T) {
	resp := &bootstrapResponse{
		ClusterID: "prod-platform",
		SeedPeers: []bootstrapPeer{
			{
				NodeName:   "core-1",
				PublicKey:  "pub-core-1",
				Endpoint:   "203.0.113.10:51820",
				AllowedIPs: []string{"10.88.0.2/32"},
				KeepAlive:  25,
			},
			{
				NodeName:   "core-2",
				PublicKey:  "pub-core-2",
				Endpoint:   "203.0.113.11:51820",
				AllowedIPs: []string{"10.88.0.3/32"},
				KeepAlive:  25,
			},
		},
		SeedServiceEndpoints: map[string][]string{
			"quartermaster": {"10.88.0.2"},
			"foghorn":       {"10.88.0.3"},
		},
	}

	body, err := renderSeedPeers(resp)
	if err != nil {
		t.Fatalf("renderSeedPeers: %v", err)
	}

	var loaded loadedStaticPeers
	if err := json.Unmarshal(body, &loaded); err != nil {
		t.Fatalf("unmarshal into agent schema: %v\n%s", err, body)
	}
	if loaded.Version == "" {
		t.Fatal("expected non-empty version")
	}
	if len(loaded.Peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(loaded.Peers))
	}
	// Per-peer DNS alias should exist so `core-1` resolves inside the mesh.
	if ips := loaded.DNS["core-1"]; len(ips) == 0 || ips[0] != "10.88.0.2" {
		t.Fatalf("expected dns[core-1]=[10.88.0.2], got %v", ips)
	}
	// Service endpoint aliases (used for `quartermaster.internal` etc.)
	if ips := loaded.DNS["quartermaster"]; len(ips) == 0 || ips[0] != "10.88.0.2" {
		t.Fatalf("expected dns[quartermaster]=[10.88.0.2], got %v", ips)
	}
}

// TestPendingEnrollment_RoundTrip exercises the resume-after-crash
// contract: pre-POST state persists on disk, can be reloaded, and
// carries the identity (node_id + keypair + token) the server's replay
// branch needs to return the same assignment.
func TestPendingEnrollment_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := pendingEnrollmentPath(dir)

	got, err := loadPendingEnrollment(path)
	if err != nil {
		t.Fatalf("load when absent: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil when file absent, got %+v", got)
	}

	pending := &pendingEnrollment{
		NodeID:     "node-abc123",
		PrivateKey: "priv-base64",
		PublicKey:  "pub-base64",
		Token:      "tok-xyz",
	}
	if writeErr := writePendingEnrollment(path, pending); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	loaded, err := loadPendingEnrollment(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.NodeID != pending.NodeID || loaded.PrivateKey != pending.PrivateKey || loaded.PublicKey != pending.PublicKey || loaded.Token != pending.Token {
		t.Fatalf("round-trip mismatch: got %+v want %+v", loaded, pending)
	}
	if loaded.StartedAt == "" {
		t.Error("expected StartedAt stamp")
	}
}

func TestEnrollmentState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "enrollment.json")
	state := &enrollmentState{
		NodeID:                "node-abc123",
		ClusterID:             "prod-platform",
		WireguardIP:           "10.88.0.42",
		WireguardPort:         51820,
		MeshCIDR:              "10.88.0.0/16",
		QuartermasterGRPCAddr: "quartermaster.internal:19002",
		StaticPeersFile:       "/etc/privateer/static-peers.json",
	}

	tmp, err := stageEnrollmentStateTmp(path, state)
	if err != nil {
		t.Fatalf("stage state: %v", err)
	}
	if renameErr := os.Rename(tmp, path); renameErr != nil {
		t.Fatalf("commit state: %v", renameErr)
	}

	loaded, err := loadEnrollmentState(path)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.NodeID != state.NodeID || loaded.WireguardIP != state.WireguardIP {
		t.Fatalf("state round-trip mismatch: %+v vs %+v", loaded, state)
	}
	if loaded.EnrolledAt == "" {
		t.Fatal("expected EnrolledAt to be stamped by stageEnrollmentStateTmp")
	}
}

func TestStagedEnrollmentCommit_HappyPath(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "enrollment.json")
	keyPath := filepath.Join(dir, "wg.key")
	peersPath := filepath.Join(dir, "static-peers.json")

	resp := &bootstrapResponse{
		NodeID:                "node-happy",
		ClusterID:             "prod-platform",
		WireguardIP:           "10.88.0.5",
		WireguardPort:         51820,
		MeshCIDR:              "10.88.0.0/16",
		QuartermasterGRPCAddr: "qm.internal:19002",
		SeedPeers: []bootstrapPeer{
			{NodeName: "core-1", PublicKey: "pk", Endpoint: "1.2.3.4:51820", AllowedIPs: []string{"10.88.0.2/32"}, KeepAlive: 25},
		},
	}
	state := &enrollmentState{
		NodeID:                resp.NodeID,
		ClusterID:             resp.ClusterID,
		WireguardIP:           resp.WireguardIP,
		WireguardPort:         int(resp.WireguardPort),
		MeshCIDR:              resp.MeshCIDR,
		QuartermasterGRPCAddr: resp.QuartermasterGRPCAddr,
		StaticPeersFile:       peersPath,
	}

	if err := stagedEnrollmentCommit(statePath, keyPath, peersPath, state, "priv-material", resp); err != nil {
		t.Fatalf("happy-path commit: %v", err)
	}

	for _, p := range []string{statePath, keyPath, peersPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to exist after commit, got: %v", p, err)
		}
	}
	// Tempfiles should be cleaned up (no .tmp left in dir).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("tempfile not cleaned up: %s", e.Name())
		}
	}
}

func TestStagedEnrollmentCommit_RollsBackOnKeyRenameFailure(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "enrollment.json")
	// keyPath points inside a subdirectory that we'll pre-create as a
	// regular file so os.Rename onto it fails predictably (destination is
	// not a directory, can't write the child path).
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(blocker, "wg.key") // parent is a file, os.MkdirAll will fail in stageKeyTmp
	peersPath := filepath.Join(dir, "static-peers.json")

	resp := &bootstrapResponse{NodeID: "node-rollback", ClusterID: "c", WireguardIP: "10.88.0.9", WireguardPort: 51820}
	state := &enrollmentState{NodeID: resp.NodeID, ClusterID: resp.ClusterID, WireguardIP: resp.WireguardIP, WireguardPort: int(resp.WireguardPort), StaticPeersFile: peersPath}

	err := stagedEnrollmentCommit(statePath, keyPath, peersPath, state, "priv", resp)
	if err == nil {
		t.Fatal("expected stagedEnrollmentCommit to fail when key staging is blocked")
	}
	// State staging happens before key staging — if state succeeded and
	// key failed during stage (not commit), we should not have committed
	// state. And we should NOT have a partially-committed enrollment on disk.
	if _, statErr := os.Stat(statePath); statErr == nil {
		t.Fatal("state was committed despite key-stage failure — rollback broken")
	}
	if _, statErr := os.Stat(peersPath); statErr == nil {
		t.Fatal("peers was committed despite key-stage failure — rollback broken")
	}
}

// TestStagedEnrollmentCommit_RollsBackOnSecondRenameFailure simulates the
// harder case: state rename succeeded but key rename fails. The first
// commit must be backed out and the error must carry the node_id plus
// the operator remediation phrase.
func TestStagedEnrollmentCommit_RollsBackOnSecondRenameFailure(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "enrollment.json")
	peersPath := filepath.Join(dir, "static-peers.json")

	// Create a directory at the key target path. os.Rename into a
	// pre-existing directory fails on Linux/macOS with EEXIST/EISDIR,
	// triggering the second-rename rollback path.
	keyPath := filepath.Join(dir, "wg.key")
	if err := os.MkdirAll(keyPath, 0o755); err != nil {
		t.Fatal(err)
	}

	resp := &bootstrapResponse{NodeID: "node-second-rename", ClusterID: "c", WireguardIP: "10.88.0.9", WireguardPort: 51820}
	state := &enrollmentState{NodeID: resp.NodeID, ClusterID: resp.ClusterID, WireguardIP: resp.WireguardIP, WireguardPort: int(resp.WireguardPort), StaticPeersFile: peersPath}

	err := stagedEnrollmentCommit(statePath, keyPath, peersPath, state, "priv", resp)
	if err == nil {
		t.Fatal("expected commit to fail when key path is a directory")
	}
	if !strings.Contains(err.Error(), "node-second-rename") {
		t.Errorf("error should name the node_id: %v", err)
	}
	if !strings.Contains(err.Error(), "replayable with the original token") {
		t.Errorf("error should carry the replay-safe remediation phrase: %v", err)
	}
	// State must not remain committed after rollback.
	if _, statErr := os.Stat(statePath); statErr == nil {
		t.Error("state should have been rolled back after key-rename failure")
	}
	// Peers never committed.
	if _, statErr := os.Stat(peersPath); statErr == nil {
		t.Error("peers should not have been committed after earlier failure")
	}
}
