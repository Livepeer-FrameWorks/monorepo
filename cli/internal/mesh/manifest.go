package mesh

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// HostWG is one row of mesh identity written back to cluster.yaml per host.
type HostWG struct {
	WireguardIP        string
	WireguardPublicKey string
	WireguardPort      int
}

// WireGuardBlock is the top-level `wireguard:` section written to cluster.yaml.
type WireGuardBlock struct {
	Enabled    bool
	MeshCIDR   string
	ListenPort int
}

// UpdateClusterYAML reads raw cluster.yaml bytes, updates per-host WG fields
// under hosts.<name>.{wireguard_ip,wireguard_public_key,wireguard_port}, and
// rewrites the top-level `wireguard:` block with enabled/mesh_cidr/listen_port.
// Other fields, comments, and key order are preserved via yaml.v3 Node edits.
func UpdateClusterYAML(raw []byte, hosts map[string]HostWG, wg WireGuardBlock) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse cluster.yaml: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, fmt.Errorf("cluster.yaml: expected single document")
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("cluster.yaml: top-level is not a mapping")
	}

	if err := updateHostsMapping(doc, hosts); err != nil {
		return nil, err
	}
	if err := upsertWireGuardBlock(doc, wg); err != nil {
		return nil, err
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return nil, fmt.Errorf("marshal cluster.yaml: %w", err)
	}
	return out, nil
}

// updateHostsMapping finds the `hosts:` mapping and upserts wireguard_ip,
// wireguard_public_key, wireguard_port on each matching host.
func updateHostsMapping(doc *yaml.Node, hosts map[string]HostWG) error {
	hostsMap := findMappingChild(doc, "hosts")
	if hostsMap == nil {
		return fmt.Errorf("cluster.yaml: 'hosts' mapping not found")
	}
	for i := 0; i+1 < len(hostsMap.Content); i += 2 {
		nameNode := hostsMap.Content[i]
		hostNode := hostsMap.Content[i+1]
		wg, ok := hosts[nameNode.Value]
		if !ok {
			continue
		}
		if hostNode.Kind != yaml.MappingNode {
			return fmt.Errorf("host %q: value is not a mapping", nameNode.Value)
		}
		setScalarField(hostNode, "wireguard_ip", wg.WireguardIP)
		setScalarField(hostNode, "wireguard_public_key", wg.WireguardPublicKey)
		setScalarField(hostNode, "wireguard_port", fmt.Sprintf("%d", wg.WireguardPort))
	}
	return nil
}

func upsertWireGuardBlock(doc *yaml.Node, wg WireGuardBlock) error {
	block := findMappingChild(doc, "wireguard")
	if block == nil {
		// Append a new top-level `wireguard:` mapping at the end of the doc.
		key := &yaml.Node{Kind: yaml.ScalarNode, Value: "wireguard", Tag: "!!str"}
		value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Content = append(doc.Content, key, value)
		block = value
	}
	setScalarField(block, "enabled", boolString(wg.Enabled))
	if wg.MeshCIDR != "" {
		setScalarField(block, "mesh_cidr", wg.MeshCIDR)
	}
	if wg.ListenPort != 0 {
		setScalarField(block, "listen_port", fmt.Sprintf("%d", wg.ListenPort))
	}
	return nil
}

// findMappingChild returns the value node for key in a mapping node, or nil.
func findMappingChild(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setScalarField upserts a scalar child at key. If the key exists, its value
// is updated in place (preserving any attached comments); otherwise a new
// key/value pair is appended to the mapping.
func setScalarField(m *yaml.Node, key, value string) {
	tag := "!!str"
	// The whole string must parse as an integer — not just a prefix — so
	// IPs like "10.88.0.2" and CIDRs like "10.88.0.0/16" stay string-tagged.
	var n int
	if value != "" {
		if _, err := fmt.Sscanf(value, "%d", &n); err == nil && fmt.Sprintf("%d", n) == value {
			tag = "!!int"
		}
	}
	if value == "true" || value == "false" {
		tag = "!!bool"
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1].Kind = yaml.ScalarNode
			m.Content[i+1].Tag = tag
			m.Content[i+1].Value = value
			m.Content[i+1].Style = 0
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value},
	)
}

// ClearAdoptedLocalMarkers removes the preserve-key markers
// (`wireguard_private_key_file` + `wireguard_private_key_managed`) from the
// named hosts in a decrypted SOPS inventory. Called during rotate when an
// `adopted_local` host is being re-keyed into SOPS — once this returns, the
// inventory represents the host as fully SOPS-managed again.
func ClearAdoptedLocalMarkers(raw []byte, hosts []string) ([]byte, error) {
	if len(hosts) == 0 {
		return raw, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse hosts.yaml: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, fmt.Errorf("hosts.yaml: expected single document")
	}
	doc := root.Content[0]
	hostsMap := findMappingChild(doc, "hosts")
	if hostsMap == nil {
		return raw, nil
	}
	target := map[string]bool{}
	for _, h := range hosts {
		target[h] = true
	}
	for i := 0; i+1 < len(hostsMap.Content); i += 2 {
		name := hostsMap.Content[i].Value
		if !target[name] {
			continue
		}
		hostNode := hostsMap.Content[i+1]
		if hostNode.Kind != yaml.MappingNode {
			continue
		}
		deleteMappingKey(hostNode, "wireguard_private_key_file")
		deleteMappingKey(hostNode, "wireguard_private_key_managed")
	}
	return yaml.Marshal(&root)
}

// deleteMappingKey removes the key/value pair with the given key from a
// mapping node in-place. No-op if the key isn't present.
func deleteMappingKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// UpdateHostInventoryYAML upserts hosts.<name>.wireguard_private_key values
// in the decrypted SOPS inventory YAML, preserving the rest of the document.
func UpdateHostInventoryYAML(raw []byte, privateKeys map[string]string) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse hosts.yaml: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, fmt.Errorf("hosts.yaml: expected single document")
	}
	doc := root.Content[0]
	hostsMap := findMappingChild(doc, "hosts")
	if hostsMap == nil {
		return nil, fmt.Errorf("hosts.yaml: 'hosts' mapping not found")
	}
	for i := 0; i+1 < len(hostsMap.Content); i += 2 {
		nameNode := hostsMap.Content[i]
		hostNode := hostsMap.Content[i+1]
		priv, ok := privateKeys[nameNode.Value]
		if !ok {
			continue
		}
		if hostNode.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("host %q: value is not a mapping", nameNode.Value)
		}
		setScalarField(hostNode, "wireguard_private_key", priv)
	}
	return yaml.Marshal(&root)
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// AdoptedHost describes a runtime-enrolled node being written into GitOps by
// `frameworks mesh reconcile --write-gitops`. The public identity goes into
// cluster.yaml; the inventory placeholder tells Ansible to preserve the
// on-disk private key instead of rendering a SOPS-managed one.
type AdoptedHost struct {
	Name               string
	ClusterID          string // cluster key in manifest.clusters
	Roles              []string
	ExternalIP         string
	WireguardIP        string
	WireguardPublicKey string
	WireguardPort      int
	NodeType           string // "core", "edge", etc. — written as tag/role metadata
	PrivateKeyFile     string // typically /etc/privateer/wg.key
}

// InsertAdoptedHostsIntoClusterYAML appends new host entries under `hosts:`
// for each AdoptedHost that isn't already present. Existing hosts are left
// untouched — use UpdateClusterYAML for per-host WG field upserts.
func InsertAdoptedHostsIntoClusterYAML(raw []byte, hosts []AdoptedHost) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse cluster.yaml: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, fmt.Errorf("cluster.yaml: expected single document")
	}
	doc := root.Content[0]
	hostsMap := findMappingChild(doc, "hosts")
	if hostsMap == nil {
		// No `hosts:` mapping — create an empty one.
		key := &yaml.Node{Kind: yaml.ScalarNode, Value: "hosts", Tag: "!!str"}
		value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Content = append(doc.Content, key, value)
		hostsMap = value
	}
	existing := map[string]bool{}
	for i := 0; i+1 < len(hostsMap.Content); i += 2 {
		existing[hostsMap.Content[i].Value] = true
	}
	for _, h := range hosts {
		if existing[h.Name] {
			continue
		}
		hostNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if h.ExternalIP != "" {
			setScalarField(hostNode, "external_ip", h.ExternalIP)
		}
		if h.ClusterID != "" {
			setScalarField(hostNode, "cluster", h.ClusterID)
		}
		if h.WireguardIP != "" {
			setScalarField(hostNode, "wireguard_ip", h.WireguardIP)
		}
		if h.WireguardPublicKey != "" {
			setScalarField(hostNode, "wireguard_public_key", h.WireguardPublicKey)
		}
		if h.WireguardPort > 0 {
			setScalarField(hostNode, "wireguard_port", fmt.Sprintf("%d", h.WireguardPort))
		}
		if len(h.Roles) > 0 {
			rolesNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
			for _, r := range h.Roles {
				rolesNode.Content = append(rolesNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: r})
			}
			hostNode.Content = append(hostNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "roles"},
				rolesNode,
			)
		}
		hostsMap.Content = append(hostsMap.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: h.Name},
			hostNode,
		)
	}
	return yaml.Marshal(&root)
}

// InsertAdoptedHostsIntoInventoryYAML appends host entries under `hosts:` in
// the decrypted SOPS inventory, marking each as externally-managed so the
// Ansible role preserves the on-disk private key. Existing hosts with a
// `wireguard_private_key` are left untouched.
func InsertAdoptedHostsIntoInventoryYAML(raw []byte, hosts []AdoptedHost) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse hosts.yaml: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, fmt.Errorf("hosts.yaml: expected single document")
	}
	doc := root.Content[0]
	hostsMap := findMappingChild(doc, "hosts")
	if hostsMap == nil {
		key := &yaml.Node{Kind: yaml.ScalarNode, Value: "hosts", Tag: "!!str"}
		value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Content = append(doc.Content, key, value)
		hostsMap = value
	}
	existing := map[string]*yaml.Node{}
	for i := 0; i+1 < len(hostsMap.Content); i += 2 {
		existing[hostsMap.Content[i].Value] = hostsMap.Content[i+1]
	}
	for _, h := range hosts {
		hostNode, ok := existing[h.Name]
		if ok && hostNode.Kind == yaml.MappingNode {
			if findMappingChild(hostNode, "wireguard_private_key") != nil {
				// Already has a SOPS-managed key — leave it alone.
				continue
			}
			setScalarField(hostNode, "wireguard_private_key_file", defaultKeyFile(h.PrivateKeyFile))
			setScalarField(hostNode, "wireguard_private_key_managed", "false")
			continue
		}
		hostNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if h.ExternalIP != "" {
			setScalarField(hostNode, "external_ip", h.ExternalIP)
		}
		setScalarField(hostNode, "wireguard_private_key_file", defaultKeyFile(h.PrivateKeyFile))
		setScalarField(hostNode, "wireguard_private_key_managed", "false")
		hostsMap.Content = append(hostsMap.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: h.Name},
			hostNode,
		)
	}
	return yaml.Marshal(&root)
}

func defaultKeyFile(s string) string {
	if s != "" {
		return s
	}
	return "/etc/privateer/wg.key"
}
