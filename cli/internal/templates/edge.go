// Package templates renders the operator-local view of the edge stack
// (compose + Caddyfile + .edge.env) for `frameworks edge init`. This is
// the "generate files for manual docker-compose up" workflow.
//
// The remote-apply path uses the Jinja equivalents under
// ansible/collections/ansible_collections/frameworks/infra/roles/edge/templates/.
// The two surfaces must stay shape-compatible: same service names, same
// env keys, same ports, same bootstrap Caddyfile semantics (tls internal +
// 503). The consistency test in edge_parity_test.go pins the invariants
// so renames on either side fail the build.
package templates

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/maintenance"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

//go:embed edge/*
var edgeFS embed.FS

type EdgeVars struct {
	NodeID          string
	EdgeDomain      string
	AcmeEmail       string
	FoghornGRPCAddr string
	EnrollmentToken string
	GRPCTLSCAPath   string
	CABundlePEM     string
	// Optional: file-based TLS certificate paths (if using Navigator-issued certs)
	CertPath string // e.g., /etc/frameworks/certs/cert.pem
	KeyPath  string // e.g., /etc/frameworks/certs/key.pem
	// Deployment mode: "container" (default; single edge image under
	// s6-overlay) or "native" (bare metal with systemd/launchd). "docker" is
	// accepted as a deprecated alias for container.
	Mode string
	// EdgeOS selects the container compose flavor: "linux" (host networking;
	// host node_tuning sysctls apply directly) or "darwin" (bridge with the
	// bounded published port set + privileged VM-tuning oneshot, because
	// Docker Desktop host networking has broken UDP semantics on macOS).
	EdgeOS           string
	HelmsmanUpstream string // localhost:18007 in both modes (loopback inside the container)
	MistUpstream     string // localhost:8080 in both modes
	CaddyAdminAddr   string // Container: "unix//run/caddy/admin.sock", Native: "localhost:2019"
	SiteAddress      string // Caddy site address: "*.cluster.root" (wildcard) or "edge.cluster.root" (single)
	MistAPIPassword  string // MistServer API auth password (used for -a flag and helmsman config sync)
	EdgeImage        string // Single edge image (helmsman+Mist+Caddy); manifest-pinned (image@digest) when a release is selected.
	ChandlerUpstream string // localhost:18020 in both modes
	TelemetryURL     string
	TelemetryToken   string
	// RelayTrustedCIDR is the CIDR whose RemoteAddr bypasses the relay
	// authorize gate for the local Mist→Helmsman hop. Empty in both modes
	// (Mist reaches Helmsman on loopback — native host or inside the edge
	// container). Never covers peer nodes.
	RelayTrustedCIDR string
	// StorageCapacityBytes optionally caps the hot-storage logical size
	// (HELMSMAN_STORAGE_CAPACITY_BYTES). Recommended on macOS where the
	// named volume reports the whole Docker VM disk.
	StorageCapacityBytes string
}

// NormalizeEdgeMode maps user-facing mode names onto the canonical set,
// folding the deprecated "docker" alias into "container".
func NormalizeEdgeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "docker", "container":
		return "container"
	case "native":
		return "native"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

// SetModeDefaults fills Mode-dependent fields if not explicitly set.
func (v *EdgeVars) SetModeDefaults() {
	v.Mode = NormalizeEdgeMode(v.Mode)
	if v.EdgeOS == "" {
		v.EdgeOS = "linux"
	}
	// Both modes are loopback now: native processes share the host, the
	// container mode shares one network namespace.
	if v.HelmsmanUpstream == "" {
		v.HelmsmanUpstream = "localhost:18007"
	}
	if v.MistUpstream == "" {
		v.MistUpstream = "localhost:8080"
	}
	if v.CaddyAdminAddr == "" {
		if v.Mode == "native" {
			v.CaddyAdminAddr = "localhost:2019"
		} else {
			v.CaddyAdminAddr = "unix//run/caddy/admin.sock"
		}
	}
	if v.ChandlerUpstream == "" {
		v.ChandlerUpstream = "localhost:18020"
	}
	if v.EdgeImage == "" {
		v.EdgeImage = "livepeerframeworks/frameworks-edge:latest"
	}
}

// edgeNetworkBlock renders the compose network settings for the edge
// service: host networking on Linux; on macOS a bridge with the bounded
// published port set (Mist pins WebRTC to UDP 18203 and SRT to UDP 8889 —
// single ports, not ranges) plus namespaced sysctls.
func edgeNetworkBlock(edgeOS string) string {
	if edgeOS != "darwin" {
		return "    network_mode: host"
	}
	return `    ports:
      - "80:80"
      - "443:443"
      - "1935:1935"
      - "4200:4200"
      - "5554:5554"
      - "8080:8080"
      - "8889:8889/udp"
      - "18203:18203/udp"
    sysctls:
      net.core.somaxconn: 16384
      net.ipv4.ip_local_port_range: "10000 65535"
    depends_on:
      edge-tuning:
        condition: service_completed_successfully`
}

// edgeTuningService renders the privileged oneshot that applies the
// non-namespaced media sysctls inside the Docker Desktop Linux VM on macOS
// (values mirror ansible roles/node_tuning defaults). On Linux the host
// node_tuning role owns these, so the block is empty.
func edgeTuningService(edgeOS string) string {
	if edgeOS != "darwin" {
		return ""
	}
	return `  edge-tuning:
    image: busybox:1.37
    container_name: frameworks-edge-tuning
    privileged: true
    network_mode: host
    command:
      - sh
      - -c
      - |
        set -x
        sysctl -w net.core.rmem_max=67108864
        sysctl -w net.core.wmem_max=67108864
        sysctl -w net.core.somaxconn=16384
        sysctl -w net.ipv4.ip_local_port_range="10000 65535"
        sysctl -w net.ipv4.tcp_notsent_lowat=16384
        sysctl -w net.core.default_qdisc=fq || true
        sysctl -w net.ipv4.tcp_congestion_control=bbr || true
    restart: "no"

`
}

// storageCapacityBlock renders the optional hot-storage logical cap. On
// macOS container deployments the named volume's Statfs reports the whole
// Docker VM disk, so an explicit cap keeps the eviction thresholds honest.
func storageCapacityBlock(vars EdgeVars) string {
	if v := strings.TrimSpace(vars.StorageCapacityBytes); v != "" {
		return "\n# Logical cap for hot storage eviction thresholds (bytes)\nHELMSMAN_STORAGE_CAPACITY_BYTES=" + v + "\n"
	}
	if vars.Mode == "container" && vars.EdgeOS == "darwin" {
		return "\n# Recommended on macOS: cap hot storage so eviction thresholds track a\n# real budget instead of the whole Docker VM disk (bytes).\n#HELMSMAN_STORAGE_CAPACITY_BYTES=107374182400\n"
	}
	return ""
}

// helmsmanBindAddr keeps helmsman's :18007 (which carries the
// unauthenticated local /node/mode endpoint) on loopback everywhere except
// the darwin bridge flavor, where vmagent scrapes it by service DNS and the
// port is never host-published.
func helmsmanBindAddr(vars EdgeVars) string {
	if vars.Mode != "native" && vars.EdgeOS == "darwin" {
		return ""
	}
	return "127.0.0.1"
}

// EdgeWriteMode selects WriteEdgeTemplates' per-file write semantic.
type EdgeWriteMode int

const (
	// EdgeWriteOverwriteCheck errors if the file exists unless overwrite=true.
	EdgeWriteOverwriteCheck EdgeWriteMode = iota
	// EdgeWriteAlways writes unconditionally.
	EdgeWriteAlways
	// EdgeWriteIfMissingOrOverwrite skips silently if the file exists and overwrite=false.
	EdgeWriteIfMissingOrOverwrite
	// EdgeWriteIfMissing never overwrites an existing file, even with
	// overwrite=true — for secrets that must not rotate on re-render.
	EdgeWriteIfMissing
)

// EdgeRenderedFile is one file produced by RenderEdgeTemplates. Path is
// relative to the target directory.
type EdgeRenderedFile struct {
	Path      string
	Content   []byte
	Mode      os.FileMode
	WriteMode EdgeWriteMode
}

// RenderEdgeTemplates returns the full set of files the edge stack writes
// into the target directory, keyed by relative path. No filesystem side
// effects.
func RenderEdgeTemplates(vars EdgeVars) ([]EdgeRenderedFile, error) {
	vars.SetModeDefaults()
	// A typo'd mode must fail here, not silently render a container stack
	// (everything non-native falls through to the container templates).
	if vars.Mode != "native" && vars.Mode != "container" {
		return nil, fmt.Errorf("invalid edge mode %q (valid: container, native; 'docker' is a deprecated alias for container)", vars.Mode)
	}

	var out []EdgeRenderedFile

	// Container mode seeds the maintenance page inside the image
	// (edgeseed); only native serves it from the host filesystem.
	if vars.Mode == "native" {
		out = append(out, EdgeRenderedFile{
			Path:      "maintenance.html",
			Content:   append([]byte(nil), maintenance.HTML...),
			Mode:      0o644,
			WriteMode: EdgeWriteIfMissingOrOverwrite,
		})
	}

	if strings.TrimSpace(vars.CABundlePEM) != "" {
		out = append(out, EdgeRenderedFile{
			Path:      filepath.Join("pki", "ca.crt"),
			Content:   []byte(vars.CABundlePEM),
			Mode:      0o644,
			WriteMode: EdgeWriteAlways,
		})
	}

	vmagentServiceBlock := ""
	if strings.TrimSpace(vars.TelemetryURL) != "" && strings.TrimSpace(vars.TelemetryToken) != "" {
		out = append(out, EdgeRenderedFile{
			Path:      filepath.Join("telemetry", "token"),
			Content:   []byte(vars.TelemetryToken + "\n"),
			Mode:      0o600,
			WriteMode: EdgeWriteAlways,
		})
		// Scrape targets: with host networking (linux) vmagent shares the
		// host namespace, so localhost reaches the edge container's ports;
		// on the darwin bridge it dials the edge service by name.
		scrapeHost := "localhost"
		vmagentNetworkBlock := "    network_mode: host\n"
		if vars.EdgeOS == "darwin" {
			scrapeHost = "edge"
			vmagentNetworkBlock = ""
		}
		vmagentConfig := fmt.Sprintf(`global:
  scrape_interval: 30s
scrape_configs:
  - job_name: edge-mist
    metrics_path: %s
    static_configs:
      - targets:
          - "%s:8080"
        labels:
          frameworks_mode: "edge"
          frameworks_node: %q
          frameworks_service: "mistserver"
  - job_name: edge-helmsman
    metrics_path: /metrics
    static_configs:
      - targets:
          - "%s:18007"
        labels:
          frameworks_mode: "edge"
          frameworks_node: %q
          frameworks_service: "helmsman"
`, mist.MetricsPath, scrapeHost, vars.NodeID, scrapeHost, vars.NodeID)
		out = append(out, EdgeRenderedFile{
			Path:      "vmagent-edge.yml",
			Content:   []byte(vmagentConfig),
			Mode:      0o644,
			WriteMode: EdgeWriteAlways,
		})
		// Loopback listener: with host networking anything else would
		// expose vmagent's control/metrics endpoint on the public edge
		// host. Nothing dials vmagent — it scrapes and remote-writes
		// outbound.
		vmagentServiceBlock = `  vmagent:
    image: victoriametrics/vmagent:v1.143.0
    container_name: frameworks-edge-vmagent
` + vmagentNetworkBlock + `    command:
      - -httpListenAddr=127.0.0.1:8429
      - -promscrape.config=/etc/frameworks/vmagent-edge.yml
      - -remoteWrite.url={{TELEMETRY_URL}}
      - -remoteWrite.bearerTokenFile=/etc/frameworks/telemetry/token
    volumes:
      - ./vmagent-edge.yml:/etc/frameworks/vmagent-edge.yml:ro
      - ./telemetry:/etc/frameworks/telemetry:ro
    restart: unless-stopped

`
	}

	// The enrollment token lives in its own write-once file so a fresh token
	// on re-render never changes .edge.env (compose recreates the helmsman
	// container on env changes; Foghorn ignores tokens for enrolled nodes).
	out = append(out, EdgeRenderedFile{
		Path:      ".edge-enroll.env",
		Content:   []byte("# Fill the enrollment token issued by FrameWorks\nEDGE_ENROLLMENT_TOKEN=" + vars.EnrollmentToken + "\n"),
		Mode:      0o600,
		WriteMode: EdgeWriteIfMissingOrOverwrite,
	})

	// Secrets stay out of the world-readable compose file: the container
	// loads them via this 0600 env file. Strictly write-once (even with
	// --overwrite) so re-renders never rotate a live MistServer password;
	// delete the file to rotate.
	if vars.Mode != "native" {
		out = append(out, EdgeRenderedFile{
			Path:      ".edge-secrets.env",
			Content:   []byte("# Shared MistServer controller password (mist -a / helmsman API client).\n# Write-once: delete this file and re-run edge init to rotate.\nMIST_API_PASSWORD=" + vars.MistAPIPassword + "\n"),
			Mode:      0o600,
			WriteMode: EdgeWriteIfMissing,
		})
	}

	// Native mode renders the bootstrap Caddyfile on the host; the container
	// image bakes its own bootstrap (edge/rootfs) and helmsman owns the
	// activated config on the caddy_etc volume, so container mode renders
	// only compose + env.
	tplFiles := []struct{ in, out string }{
		{"edge/.edge.env.tmpl", ".edge.env"},
	}
	if vars.Mode == "native" {
		tplFiles = append(tplFiles, struct{ in, out string }{"edge/Caddyfile.tmpl", "Caddyfile"})
	} else {
		tplFiles = append([]struct{ in, out string }{
			{"edge/docker-compose.edge.yml.tmpl", "docker-compose.edge.yml"},
		}, tplFiles...)
	}
	for _, f := range tplFiles {
		b, err := edgeFS.ReadFile(f.in)
		if err != nil {
			return nil, err
		}
		content := string(b)
		content = strings.ReplaceAll(content, "{{NODE_ID}}", vars.NodeID)
		content = strings.ReplaceAll(content, "{{EDGE_DOMAIN}}", vars.EdgeDomain)
		content = strings.ReplaceAll(content, "{{ACME_EMAIL}}", vars.AcmeEmail)
		content = strings.ReplaceAll(content, "{{FOGHORN_GRPC_ADDR}}", vars.FoghornGRPCAddr)
		content = strings.ReplaceAll(content, "{{ENROLLMENT_TOKEN}}", vars.EnrollmentToken)
		content = strings.ReplaceAll(content, "{{GRPC_TLS_CA_PATH}}", vars.GRPCTLSCAPath)
		content = strings.ReplaceAll(content, "{{CERT_PATH}}", vars.CertPath)
		content = strings.ReplaceAll(content, "{{KEY_PATH}}", vars.KeyPath)
		content = strings.ReplaceAll(content, "{{HELMSMAN_UPSTREAM}}", vars.HelmsmanUpstream)
		content = strings.ReplaceAll(content, "{{MIST_UPSTREAM}}", vars.MistUpstream)
		content = strings.ReplaceAll(content, "{{CADDY_ADMIN_ADDR}}", vars.CaddyAdminAddr)
		content = strings.ReplaceAll(content, "{{SITE_ADDRESS}}", vars.SiteAddress)
		content = strings.ReplaceAll(content, "{{DEPLOY_MODE}}", vars.Mode)
		content = strings.ReplaceAll(content, "{{RELAY_TRUSTED_CIDR}}", vars.RelayTrustedCIDR)
		content = strings.ReplaceAll(content, "{{MIST_API_PASSWORD}}", vars.MistAPIPassword)
		content = strings.ReplaceAll(content, "{{EDGE_IMAGE}}", vars.EdgeImage)
		content = strings.ReplaceAll(content, "{{EDGE_NETWORK_BLOCK}}", edgeNetworkBlock(vars.EdgeOS))
		content = strings.ReplaceAll(content, "{{EDGE_TUNING_SERVICE}}", edgeTuningService(vars.EdgeOS))
		content = strings.ReplaceAll(content, "{{CHANDLER_UPSTREAM}}", vars.ChandlerUpstream)
		content = strings.ReplaceAll(content, "{{TELEMETRY_URL}}", vars.TelemetryURL)
		content = strings.ReplaceAll(content, "{{VMAGENT_EDGE_SERVICE}}", vmagentServiceBlock)
		content = strings.ReplaceAll(content, "{{TELEMETRY_TOKEN}}", vars.TelemetryToken)
		content = strings.ReplaceAll(content, "{{STORAGE_CAPACITY_BLOCK}}", storageCapacityBlock(vars))
		content = strings.ReplaceAll(content, "{{HELMSMAN_BIND_ADDR}}", helmsmanBindAddr(vars))
		if vars.CertPath != "" && vars.KeyPath != "" {
			content = strings.ReplaceAll(content, "{{TLS_DIRECTIVE}}", fmt.Sprintf("tls %s %s", vars.CertPath, vars.KeyPath))
		} else {
			content = strings.ReplaceAll(content, "{{TLS_DIRECTIVE}}", "")
		}
		out = append(out, EdgeRenderedFile{
			Path:      f.out,
			Content:   []byte(content),
			Mode:      0o644,
			WriteMode: EdgeWriteOverwriteCheck,
		})
	}

	return out, nil
}

// WriteEdgeTemplates writes edge stack templates into the target directory.
// It will not overwrite existing files unless overwrite is true.
func WriteEdgeTemplates(targetDir string, vars EdgeVars, overwrite bool) error {
	files, err := RenderEdgeTemplates(vars)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		outPath := filepath.Join(targetDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		switch f.WriteMode {
		case EdgeWriteAlways:
			if err := os.WriteFile(outPath, f.Content, f.Mode); err != nil {
				return err
			}
		case EdgeWriteIfMissingOrOverwrite:
			if _, statErr := os.Stat(outPath); statErr != nil || overwrite {
				if err := os.WriteFile(outPath, f.Content, f.Mode); err != nil {
					return err
				}
			}
		case EdgeWriteIfMissing:
			if _, statErr := os.Stat(outPath); statErr != nil {
				if err := os.WriteFile(outPath, f.Content, f.Mode); err != nil {
					return err
				}
			}
		case EdgeWriteOverwriteCheck:
			if _, statErr := os.Stat(outPath); statErr == nil && !overwrite {
				return fmt.Errorf("file exists: %s (use overwrite)", outPath)
			}
			if err := os.WriteFile(outPath, f.Content, f.Mode); err != nil {
				return err
			}
		}
	}
	return nil
}
