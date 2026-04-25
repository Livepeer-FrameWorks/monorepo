package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frameworks/api_mesh/internal/wireguard"
)

// staticPeersFile is the JSON shape produced by Ansible's privateer role.
type staticPeersFile struct {
	Version string              `json:"version"`
	Peers   []staticPeer        `json:"peers"`
	DNS     map[string][]string `json:"dns,omitempty"`
}

type staticPeer struct {
	Name       string   `json:"name"`
	PublicKey  string   `json:"public_key"`
	AllowedIPs []string `json:"allowed_ips"`
	Endpoint   string   `json:"endpoint,omitempty"`
	KeepAlive  int      `json:"keep_alive,omitempty"`
}

// lastKnownMesh is the JSON persisted to {DataDir}/last_known_mesh.json after
// every successful mesh apply. It lets a rebooted agent reconstitute wg0
// before Quartermaster is reachable again.
type lastKnownMesh struct {
	Source      string              `json:"source"` // "seed" | "dynamic"
	Version     string              `json:"version"`
	WireguardIP string              `json:"wireguard_ip"`
	ListenPort  int                 `json:"listen_port"`
	Peers       []lastKnownPeer     `json:"peers"`
	DNS         map[string][]string `json:"dns,omitempty"`
	WrittenAt   string              `json:"written_at"`
}

type lastKnownPeer struct {
	Name       string   `json:"name,omitempty"`
	PublicKey  string   `json:"public_key"`
	AllowedIPs []string `json:"allowed_ips"`
	Endpoint   string   `json:"endpoint,omitempty"`
	KeepAlive  int      `json:"keep_alive,omitempty"`
}

// loadStaticPeers reads and hashes the Ansible-rendered static-peers.json.
// Returns (nil, nil) if the file doesn't exist.
func loadStaticPeers(path string) (*staticPeersFile, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read static peers %s: %w", path, err)
	}
	var sp staticPeersFile
	if err := json.Unmarshal(data, &sp); err != nil {
		return nil, fmt.Errorf("parse static peers %s: %w", path, err)
	}
	if sp.Version == "" {
		sp.Version = hashStaticMesh(sp.Peers, sp.DNS)
	}
	return &sp, nil
}

// loadLastKnown reads the persisted snapshot, or (nil, nil) if absent.
func loadLastKnown(path string) (*lastKnownMesh, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read last-known %s: %w", path, err)
	}
	var lk lastKnownMesh
	if err := json.Unmarshal(data, &lk); err != nil {
		return nil, fmt.Errorf("parse last-known %s: %w", path, err)
	}
	return &lk, nil
}

// writeLastKnown atomically writes the snapshot (temp + rename).
func writeLastKnown(path string, lk *lastKnownMesh) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	lk.WrittenAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.MarshalIndent(lk, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal last-known: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".last-known-*.json")
	if err != nil {
		return fmt.Errorf("temp last-known: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write last-known: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close last-known: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o640); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod last-known: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename last-known: %w", err)
	}
	return nil
}

// hashStaticMesh produces a stable fallback version for static seed state when
// static-peers.json does not already carry an embedded version string.
func hashStaticMesh(peers []staticPeer, dns map[string][]string) string {
	sorted := make([]staticPeer, len(peers))
	copy(sorted, peers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PublicKey < sorted[j].PublicKey })
	h := sha256.New()
	for _, p := range sorted {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d\n",
			p.Name, p.PublicKey, p.Endpoint, strings.Join(p.AllowedIPs, ","), p.KeepAlive)
	}
	dnsNames := make([]string, 0, len(dns))
	for name := range dns {
		dnsNames = append(dnsNames, name)
	}
	sort.Strings(dnsNames)
	for _, name := range dnsNames {
		ips := append([]string(nil), dns[name]...)
		sort.Strings(ips)
		fmt.Fprintf(h, "dns\x00%s\x00%s\n", name, strings.Join(ips, ","))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// readPrivateKey reads the base64-encoded WG private key from disk.
func readPrivateKey(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("private key path not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read private key %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// staticPeersToWireGuard converts the GitOps peer schema into the runtime
// wireguard.Peer shape the manager expects.
func staticPeersToWireGuard(peers []staticPeer) []wireguard.Peer {
	out := make([]wireguard.Peer, len(peers))
	for i, p := range peers {
		ka := p.KeepAlive
		if ka == 0 {
			ka = 25
		}
		out[i] = wireguard.Peer{
			PublicKey:  p.PublicKey,
			Endpoint:   p.Endpoint,
			AllowedIPs: p.AllowedIPs,
			KeepAlive:  ka,
		}
	}
	return out
}

// lastKnownToWireGuard reconstructs a wireguard.Config from a persisted peer
// list, combining it with the agent's authoritative self identity (private
// key, mesh IP, listen port). lk.WireguardIP and lk.ListenPort are ignored
// — they are kept in the snapshot for diagnostics only. This is the
// invariant that lets GitOps key rotation propagate on reboot: a stale
// snapshot cannot resurrect the old self-address.
func lastKnownToWireGuard(lk *lastKnownMesh, privateKey, selfIP string, selfPort int) wireguard.Config {
	peers := make([]wireguard.Peer, len(lk.Peers))
	for i, p := range lk.Peers {
		peers[i] = wireguard.Peer{
			PublicKey:  p.PublicKey,
			Endpoint:   p.Endpoint,
			AllowedIPs: p.AllowedIPs,
			KeepAlive:  p.KeepAlive,
		}
	}
	return wireguard.Config{
		PrivateKey: privateKey,
		Address:    fmt.Sprintf("%s/32", selfIP),
		ListenPort: selfPort,
		Peers:      peers,
	}
}
