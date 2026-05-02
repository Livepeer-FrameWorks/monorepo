package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frameworks/api_mesh/internal/wireguard"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
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

func mergeLastKnownWithSeed(lk *lastKnownMesh, sp *staticPeersFile) *lastKnownMesh {
	if lk == nil {
		lk = &lastKnownMesh{}
	}
	merged := &lastKnownMesh{
		Source:      lk.Source,
		Version:     lk.Version,
		WireguardIP: lk.WireguardIP,
		ListenPort:  lk.ListenPort,
		Peers:       mergeLastKnownPeers(staticToLastKnownPeers(staticSeedPeers(sp)), lk.Peers),
		DNS:         mergeDNSRecords(staticSeedDNS(sp), lk.DNS),
	}
	return merged
}

func staticSeedPeers(sp *staticPeersFile) []staticPeer {
	if sp == nil {
		return nil
	}
	return sp.Peers
}

func staticSeedDNS(sp *staticPeersFile) map[string][]string {
	if sp == nil {
		return nil
	}
	return sp.DNS
}

func mergeLastKnownPeers(base, overlay []lastKnownPeer) []lastKnownPeer {
	out := make([]lastKnownPeer, 0, len(base)+len(overlay))
	positions := make(map[string]int, len(base)+len(overlay))
	for _, peer := range base {
		key := strings.TrimSpace(peer.PublicKey)
		if key == "" {
			continue
		}
		positions[key] = len(out)
		out = append(out, copyLastKnownPeer(peer))
	}
	for _, peer := range overlay {
		key := strings.TrimSpace(peer.PublicKey)
		if key == "" {
			continue
		}
		copied := copyLastKnownPeer(peer)
		if pos, ok := positions[key]; ok {
			out[pos] = copied
			continue
		}
		positions[key] = len(out)
		out = append(out, copied)
	}
	return out
}

func copyLastKnownPeer(peer lastKnownPeer) lastKnownPeer {
	peer.AllowedIPs = append([]string(nil), peer.AllowedIPs...)
	return peer
}

func mergeDNSRecords(base, overlay map[string][]string) map[string][]string {
	out := make(map[string][]string, len(base)+len(overlay))
	for name, ips := range base {
		out[name] = append([]string(nil), ips...)
	}
	for name, ips := range overlay {
		out[name] = append([]string(nil), ips...)
	}
	return out
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

// parsePeerStrings parses the wire-format strings carried by GitOps JSON,
// the persisted last-known snapshot, and the Quartermaster proto into a
// typed wireguard.Peer. This is the boundary parse — internal code reads
// typed values only.
func parsePeerStrings(label, publicKey, endpoint string, allowedIPs []string, keepAlive int) (wireguard.Peer, error) {
	pub, err := wgtypes.ParseKey(strings.TrimSpace(publicKey))
	if err != nil {
		return wireguard.Peer{}, fmt.Errorf("peer %s: parse public key: %w", label, err)
	}

	var ep *net.UDPAddr
	if e := strings.TrimSpace(endpoint); e != "" {
		ap, parseErr := netip.ParseAddrPort(e)
		if parseErr != nil {
			return wireguard.Peer{}, fmt.Errorf("peer %s: parse endpoint %q: %w", label, e, parseErr)
		}
		ep = net.UDPAddrFromAddrPort(ap)
	}

	nets := make([]net.IPNet, 0, len(allowedIPs))
	for _, s := range allowedIPs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		_, n, parseErr := net.ParseCIDR(s)
		if parseErr != nil {
			return wireguard.Peer{}, fmt.Errorf("peer %s: parse allowed_ip %q: %w", label, s, parseErr)
		}
		nets = append(nets, *n)
	}

	return wireguard.Peer{
		PublicKey:  pub,
		Endpoint:   ep,
		AllowedIPs: nets,
		KeepAlive:  keepAlive,
	}, nil
}

// staticPeersToWireGuard converts the GitOps peer schema into the runtime
// wireguard.Peer shape, parsing each string into typed values. KeepAlive
// defaults to 25s when unset upstream.
func staticPeersToWireGuard(peers []staticPeer) ([]wireguard.Peer, error) {
	out := make([]wireguard.Peer, 0, len(peers))
	for _, p := range peers {
		ka := p.KeepAlive
		if ka == 0 {
			ka = 25
		}
		label := p.Name
		if label == "" {
			label = p.PublicKey
		}
		peer, err := parsePeerStrings(label, p.PublicKey, p.Endpoint, p.AllowedIPs, ka)
		if err != nil {
			return nil, err
		}
		out = append(out, peer)
	}
	return out, nil
}

// selfConfig builds the typed Config for the agent's own identity (private
// key + mesh address + listen port). Used by both the seed and last-known
// paths. The caller adds peers separately.
func selfConfig(privateKey, selfIP string, selfPort int) (wireguard.Config, error) {
	priv, err := wgtypes.ParseKey(strings.TrimSpace(privateKey))
	if err != nil {
		return wireguard.Config{}, fmt.Errorf("parse private key: %w", err)
	}
	addr, err := netip.ParsePrefix(fmt.Sprintf("%s/32", selfIP))
	if err != nil {
		return wireguard.Config{}, fmt.Errorf("parse self address %s/32: %w", selfIP, err)
	}
	return wireguard.Config{
		PrivateKey: priv,
		Address:    addr,
		ListenPort: selfPort,
	}, nil
}

// lastKnownToWireGuard reconstructs a wireguard.Config from a persisted peer
// list, combining it with the agent's authoritative self identity (private
// key, mesh IP, listen port). lk.WireguardIP and lk.ListenPort are ignored
// — they are kept in the snapshot for diagnostics only. This is the
// invariant that lets GitOps key rotation propagate on reboot: a stale
// snapshot cannot resurrect the old self-address.
func lastKnownToWireGuard(lk *lastKnownMesh, privateKey, selfIP string, selfPort int) (wireguard.Config, error) {
	cfg, err := selfConfig(privateKey, selfIP, selfPort)
	if err != nil {
		return wireguard.Config{}, err
	}
	peers, err := lastKnownPeersToWireGuard(lk.Peers)
	if err != nil {
		return wireguard.Config{}, err
	}
	cfg.Peers = peers
	return cfg, nil
}

func lastKnownPeersToWireGuard(in []lastKnownPeer) ([]wireguard.Peer, error) {
	peers := make([]wireguard.Peer, 0, len(in))
	for _, p := range in {
		label := p.Name
		if label == "" {
			label = p.PublicKey
		}
		peer, err := parsePeerStrings(label, p.PublicKey, p.Endpoint, p.AllowedIPs, p.KeepAlive)
		if err != nil {
			return nil, err
		}
		peers = append(peers, peer)
	}
	return peers, nil
}
