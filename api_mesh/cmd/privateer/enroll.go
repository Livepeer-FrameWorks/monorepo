package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	pkgmesh "frameworks/pkg/mesh"

	"github.com/google/uuid"
)

func contentHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// enrollmentState is the mesh identity the control plane assigned to this
// node. Persisted on disk after a successful enrollment so subsequent
// reboots can start Privateer without re-enrolling. Contains no bearer
// credentials — SERVICE_TOKEN lives in /etc/privateer/privateer.env,
// delivered by the operator (Ansible for seed nodes, `mesh join` for
// runtime-enrolled nodes).
type enrollmentState struct {
	NodeID                string `json:"node_id"`
	ClusterID             string `json:"cluster_id"`
	WireguardIP           string `json:"wireguard_ip"`
	WireguardPort         int    `json:"wireguard_port"`
	MeshCIDR              string `json:"mesh_cidr,omitempty"`
	QuartermasterGRPCAddr string `json:"quartermaster_grpc_addr"`
	StaticPeersFile       string `json:"static_peers_file"`
	EnrolledAt            string `json:"enrolled_at"`
}

// pendingEnrollment is the intent-to-enroll state we persist before the
// bootstrap RPC, so a crash between server commit and local commit is
// recoverable. On retry we load the same node_id + keypair + token; the
// server's replay branch (keyed on token + node_id + public_key) returns
// the already-assigned identity without consuming a fresh token.
//
// Lives at 0600 next to enrollment.json. Removed on successful commit.
type pendingEnrollment struct {
	NodeID     string `json:"node_id"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	Token      string `json:"token"`
	StartedAt  string `json:"started_at"`
}

func pendingEnrollmentPath(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "/var/lib/privateer"
	}
	return filepath.Join(dataDir, "pending-enrollment.json")
}

func loadPendingEnrollment(path string) (*pendingEnrollment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read pending-enrollment: %w", err)
	}
	var p pendingEnrollment
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse pending-enrollment: %w", err)
	}
	return &p, nil
}

func writePendingEnrollment(path string, p *pendingEnrollment) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	p.StartedAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pending-*.json.tmp")
	if err != nil {
		return fmt.Errorf("tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// enrollmentStatePath is the on-disk location of the persisted enrollment
// state. Keeping it alongside last_known_mesh.json under DataDir means the
// two have the same lifecycle (backups, wipes, etc.).
func enrollmentStatePath(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "/var/lib/privateer"
	}
	return filepath.Join(dataDir, "enrollment.json")
}

// loadEnrollmentState returns the persisted state or (nil, nil) if absent.
// Missing file is not an error — the node may be seed-managed rather than
// enrolled.
func loadEnrollmentState(path string) (*enrollmentState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read enrollment state %s: %w", path, err)
	}
	var s enrollmentState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse enrollment state %s: %w", path, err)
	}
	return &s, nil
}

// writeEnrollmentState atomically persists the state via tempfile + rename.
// tryEnrollIfNeeded runs the local-keygen enrollment flow when no WireGuard
// private key exists on disk AND a join token is present. On success all
// three artifacts (enrollment state, private key, static peers) are
// committed atomically per-file; any commit failure rolls back previously-
// renamed targets and surfaces an operator-actionable error.
//
// Returns (nil, nil) when no enrollment action is needed (key already
// present, or no token supplied).
func tryEnrollIfNeeded(ctx context.Context, logger logging.Logger, privateKeyFile, dataDir string) (*enrollmentState, error) {
	if privateKeyFile == "" {
		return nil, nil
	}
	if _, statErr := os.Stat(privateKeyFile); statErr == nil {
		return nil, nil
	} else if !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("stat %s: %w", privateKeyFile, statErr)
	}

	token := readJoinToken()
	if token == "" {
		return nil, nil
	}

	bootstrapURL := strings.TrimSpace(os.Getenv("BRIDGE_BOOTSTRAP_ADDR"))
	if bootstrapURL == "" {
		return nil, fmt.Errorf("enrollment: BRIDGE_BOOTSTRAP_ADDR is required when MESH_JOIN_TOKEN is set")
	}
	if !strings.Contains(bootstrapURL, "://") {
		bootstrapURL = "https://" + bootstrapURL
	}

	// Resume a prior in-flight enrollment if one is on disk. On a fresh
	// attempt we generate a new UUID node_id + keypair, persist them under
	// pending-enrollment.json, and only then POST. On retry (key missing,
	// pending present) we reuse the same node_id + keypair + token so the
	// server's replay branch returns the already-assigned identity instead
	// of failing on a spent token.
	pendingPath := pendingEnrollmentPath(dataDir)
	pending, err := loadPendingEnrollment(pendingPath)
	if err != nil {
		return nil, fmt.Errorf("enrollment: %w", err)
	}
	if pending == nil || pending.Token != token {
		// Either no prior attempt or the operator rotated the token since
		// the last try; either way, start fresh. Stale pending state from a
		// different token gets overwritten below.
		priv, pub, keyErr := pkgmesh.GenerateKeyPair()
		if keyErr != nil {
			return nil, fmt.Errorf("enrollment: generate keypair: %w", keyErr)
		}
		pending = &pendingEnrollment{
			NodeID:     "node-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12],
			PrivateKey: priv,
			PublicKey:  pub,
			Token:      token,
		}
		if writeErr := writePendingEnrollment(pendingPath, pending); writeErr != nil {
			return nil, fmt.Errorf("enrollment: persist pending: %w", writeErr)
		}
	}
	priv := pending.PrivateKey
	pub := pending.PublicKey
	nodeID := pending.NodeID

	hostname := strings.TrimSpace(os.Getenv("MESH_NODE_NAME"))
	if hostname == "" {
		if h, herr := os.Hostname(); herr == nil {
			hostname = h
		}
	}
	nodeType := strings.TrimSpace(os.Getenv("MESH_NODE_TYPE"))
	if nodeType == "" {
		nodeType = "core"
	}

	reqBody := map[string]any{
		"token":                token,
		"node_id":              nodeID,
		"node_type":            nodeType,
		"hostname":             hostname,
		"wireguard_public_key": pub,
	}
	if ext := strings.TrimSpace(os.Getenv("MESH_EXTERNAL_IP")); ext != "" {
		reqBody["external_ip"] = ext
	}
	if intIP := strings.TrimSpace(os.Getenv("MESH_INTERNAL_IP")); intIP != "" {
		reqBody["internal_ip"] = intIP
	}
	if cluster := strings.TrimSpace(os.Getenv("CLUSTER_ID")); cluster != "" {
		reqBody["target_cluster_id"] = cluster
	}

	resp, err := postBootstrap(ctx, bootstrapURL, reqBody)
	if err != nil {
		// Request failed before any response was decoded. If the error
		// happened before the server committed, the token is still valid
		// and retrying will go through normally. If the server committed
		// and we lost the response, the next retry will hit the replay
		// branch via (token, node_id, public_key) — pending-enrollment.json
		// holds all three, and we leave it in place so that can happen.
		return nil, fmt.Errorf("enrollment: bootstrap request: %w", err)
	}

	if strings.TrimSpace(resp.WireguardIP) == "" {
		return nil, fmt.Errorf("enrollment: bootstrap response missing wireguard_ip")
	}
	if strings.TrimSpace(resp.ClusterID) == "" {
		return nil, fmt.Errorf("enrollment: bootstrap response missing cluster_id")
	}

	staticPeersFile := strings.TrimSpace(os.Getenv("PRIVATEER_STATIC_PEERS_FILE"))
	if staticPeersFile == "" {
		staticPeersFile = "/etc/privateer/static-peers.json"
	}

	state := &enrollmentState{
		NodeID:                resp.NodeID,
		ClusterID:             resp.ClusterID,
		WireguardIP:           resp.WireguardIP,
		WireguardPort:         int(resp.WireguardPort),
		MeshCIDR:              resp.MeshCIDR,
		QuartermasterGRPCAddr: resp.QuartermasterGRPCAddr,
		StaticPeersFile:       staticPeersFile,
	}

	// Stage everything to sibling tempfiles, then commit in order. A partial
	// failure rolls back committed targets; the retry then finds wg.key
	// missing and pending-enrollment.json still present, replays with the
	// same (token, node_id, public_key) tuple, and the server's replay
	// branch returns the already-assigned identity without consuming a
	// fresh token.
	if err := stagedEnrollmentCommit(enrollmentStatePath(dataDir), privateKeyFile, staticPeersFile, state, priv, resp); err != nil {
		return nil, err
	}

	// Successful commit: remove the pending-enrollment marker so the
	// bootstrap token and staged private key don't linger on disk. If
	// removal fails (permissions, read-only mount), warn loudly — the key
	// file already exists, so subsequent starts won't re-enter enrollment,
	// but the operator needs to delete the stale secret-bearing file.
	if err := os.Remove(pendingPath); err != nil && !os.IsNotExist(err) {
		logger.WithError(err).WithField("path", pendingPath).Warn("enrollment succeeded but pending-enrollment file could not be removed; delete it manually — it carries the bootstrap token and private key from the enrollment exchange")
	}

	logger.WithFields(logging.Fields{
		"node_id":      state.NodeID,
		"cluster_id":   state.ClusterID,
		"wireguard_ip": state.WireguardIP,
	}).Info("Enrollment succeeded; mesh identity assigned by the control plane")

	return state, nil
}

// stagedEnrollmentCommit writes enrollment.json, wg.key, and static-peers.json
// to sibling tempfiles first, then renames each onto its target in the order
// state → key → peers. On any rename failure it rolls back previously-
// committed targets (best-effort os.Remove) and returns an error that names
// the node_id. A failure here is recoverable: pending-enrollment.json
// remains on disk, so the next tryEnrollIfNeeded run replays with the same
// token + node_id + public_key and the server returns the already-assigned
// identity via its replay branch.
func stagedEnrollmentCommit(statePath, keyPath, peersPath string, state *enrollmentState, priv string, resp *bootstrapResponse) error {
	// Stage all three into sibling tempfiles first. None of these touch the
	// target paths yet; we can abort with no on-disk trace.
	stateTmp, err := stageEnrollmentStateTmp(statePath, state)
	if err != nil {
		return fmt.Errorf("enrollment: stage state: %w", err)
	}
	cleanup := []string{stateTmp}
	discard := func() {
		for _, p := range cleanup {
			_ = os.Remove(p) //nolint:errcheck
		}
	}

	keyTmp, err := stageKeyTmp(keyPath, priv)
	if err != nil {
		discard()
		return fmt.Errorf("enrollment: stage key: %w", err)
	}
	cleanup = append(cleanup, keyTmp)

	peersTmp, err := stagePeersTmp(peersPath, resp)
	if err != nil {
		discard()
		return fmt.Errorf("enrollment: stage peers: %w", err)
	}
	cleanup = append(cleanup, peersTmp)

	// Commit phase: rename each tempfile onto its target. Track which
	// targets have already been promoted so we can back them out on a
	// subsequent failure.
	committed := make([]string, 0, 3)
	rollback := func(msg string, cause error) error {
		// Best-effort remove of successfully-committed targets, in reverse
		// order. Remaining tempfiles are cleaned up too.
		for i := len(committed) - 1; i >= 0; i-- {
			_ = os.Remove(committed[i]) //nolint:errcheck
		}
		discard()
		return fmt.Errorf("enrollment: %s (node_id=%s): %w — rerun `frameworks mesh join`; pending-enrollment state on disk makes the retry replayable with the original token", msg, state.NodeID, cause)
	}

	if err := os.Rename(stateTmp, statePath); err != nil {
		return rollback("commit state", err)
	}
	committed = append(committed, statePath)

	if err := os.Rename(keyTmp, keyPath); err != nil {
		return rollback("commit key", err)
	}
	committed = append(committed, keyPath)

	if err := os.Rename(peersTmp, peersPath); err != nil {
		return rollback("commit peers", err)
	}
	return nil
}

// stageEnrollmentStateTmp writes state to a sibling tempfile (0o600) and
// returns its path. Caller is responsible for renaming or removing it.
func stageEnrollmentStateTmp(target string, state *enrollmentState) (string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	state.EnrolledAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".enrollment-*.json.tmp")
	if err != nil {
		return "", fmt.Errorf("tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("chmod: %w", err)
	}
	return tmpPath, nil
}

// stageKeyTmp writes the WireGuard private key to a sibling tempfile at
// 0o600 and returns its path.
func stageKeyTmp(target, priv string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".wg-key-*.tmp")
	if err != nil {
		return "", fmt.Errorf("tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write([]byte(priv + "\n")); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("chmod: %w", err)
	}
	return tmpPath, nil
}

// stagePeersTmp renders the seed-peer JSON to a sibling tempfile (0o640)
// and returns its path.
func stagePeersTmp(target string, resp *bootstrapResponse) (string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	payload, err := renderSeedPeers(resp)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".static-peers-*.json.tmp")
	if err != nil {
		return "", fmt.Errorf("tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o640); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("chmod: %w", err)
	}
	return tmpPath, nil
}

// readJoinToken returns MESH_JOIN_TOKEN from the environment.
func readJoinToken() string {
	return strings.TrimSpace(os.Getenv("MESH_JOIN_TOKEN"))
}

// bootstrapPeer mirrors the JSON shape returned by Bridge's
// /v1/bootstrap/infrastructure-node handler (api_gateway/internal/handlers/bootstrap_infra.go).
type bootstrapPeer struct {
	NodeName   string   `json:"node_name"`
	PublicKey  string   `json:"public_key"`
	Endpoint   string   `json:"endpoint"`
	AllowedIPs []string `json:"allowed_ips"`
	KeepAlive  int32    `json:"keep_alive"`
}

type bootstrapResponse struct {
	NodeID                string              `json:"node_id"`
	TenantID              string              `json:"tenant_id,omitempty"`
	ClusterID             string              `json:"cluster_id"`
	WireguardIP           string              `json:"wireguard_ip"`
	WireguardPort         int32               `json:"wireguard_port"`
	MeshCIDR              string              `json:"mesh_cidr"`
	QuartermasterGRPCAddr string              `json:"quartermaster_grpc_addr"`
	SeedPeers             []bootstrapPeer     `json:"seed_peers"`
	SeedServiceEndpoints  map[string][]string `json:"seed_service_endpoints"`
	CABundle              string              `json:"ca_bundle,omitempty"`
}

// postBootstrap POSTs the enrollment request to Bridge and decodes the JSON
// response. Respects GRPC_ALLOW_INSECURE / GRPC_TLS_CA_PATH env hints only
// for parity with other Privateer connections; since Bridge speaks standard
// HTTPS, this is mostly about test/dev environments.
func postBootstrap(ctx context.Context, baseURL string, body map[string]any) (*bootstrapResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal bootstrap request: %w", err)
	}
	url := strings.TrimRight(baseURL, "/") + "/v1/bootstrap/infrastructure-node"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	if config.GetEnvBool("GRPC_ALLOW_INSECURE", false) && strings.HasPrefix(url, "https://") {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in via env for dev/test
		}
	}

	httpResp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer httpResp.Body.Close()

	respBody, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read bootstrap response: %w", readErr)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("bootstrap rejected (%d): %s", httpResp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out bootstrapResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode bootstrap response: %w", err)
	}
	return &out, nil
}

// renderSeedPeers builds the static-peers.json body from the bootstrap
// response, matching the shape the Privateer agent's static-peers loader
// expects (`api_mesh/internal/agent/static.go:staticPeersFile`). Shared
// between the enrollment commit path and tests that round-trip the output.
func renderSeedPeers(resp *bootstrapResponse) ([]byte, error) {
	type peer struct {
		Name       string   `json:"name"`
		PublicKey  string   `json:"public_key"`
		AllowedIPs []string `json:"allowed_ips"`
		Endpoint   string   `json:"endpoint,omitempty"`
		KeepAlive  int      `json:"keep_alive,omitempty"`
	}
	type doc struct {
		Version string              `json:"version"`
		Peers   []peer              `json:"peers"`
		DNS     map[string][]string `json:"dns,omitempty"`
	}
	out := doc{DNS: map[string][]string{}}
	for _, p := range resp.SeedPeers {
		out.Peers = append(out.Peers, peer{
			Name:       p.NodeName,
			PublicKey:  p.PublicKey,
			AllowedIPs: p.AllowedIPs,
			Endpoint:   p.Endpoint,
			KeepAlive:  int(p.KeepAlive),
		})
		// Per-peer DNS alias so peer hostnames resolve to their mesh IPs.
		for _, allowed := range p.AllowedIPs {
			if ip, ok := trimCIDRSuffix(allowed); ok && p.NodeName != "" {
				out.DNS[p.NodeName] = appendUnique(out.DNS[p.NodeName], ip)
			}
		}
	}
	for svcType, ips := range resp.SeedServiceEndpoints {
		if svcType == "" {
			continue
		}
		for _, ip := range ips {
			out.DNS[svcType] = appendUnique(out.DNS[svcType], ip)
		}
	}

	// Deterministic version: sha256 of peers+dns to match Ansible's
	// to_json | hash('sha256') pattern close enough for the loader, which
	// only uses version as an opaque etag.
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	out.Version = contentHash(body)

	final, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(final, '\n'), nil
}

func trimCIDRSuffix(s string) (string, bool) {
	idx := strings.Index(s, "/")
	if idx < 0 {
		return s, true
	}
	return s[:idx], idx > 0
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
