package templates

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/pkg/maintenance"
	"frameworks/pkg/mist"
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
	// Deployment mode: "docker" (default) or "native" (bare metal with systemd)
	Mode             string
	HelmsmanUpstream string // Docker: "helmsman:18007", Native: "localhost:18007"
	MistUpstream     string // Docker: "mistserver:8080", Native: "localhost:8080"
	CaddyAdminAddr   string // Docker: "unix//run/caddy/admin.sock", Native: "localhost:2019"
	SiteAddress      string // Caddy site address: "*.cluster.root" (wildcard) or "edge.cluster.root" (single)
	MistAPIPassword  string // MistServer API auth password (used for -a flag and helmsman config sync)
	ChandlerUpstream string // Docker: "chandler:18020", Native: "localhost:18020"
	TelemetryURL     string
	TelemetryToken   string
}

// SetModeDefaults fills Mode-dependent fields if not explicitly set.
func (v *EdgeVars) SetModeDefaults() {
	if v.Mode == "" {
		v.Mode = "docker"
	}
	if v.HelmsmanUpstream == "" {
		if v.Mode == "native" {
			v.HelmsmanUpstream = "localhost:18007"
		} else {
			v.HelmsmanUpstream = "helmsman:18007"
		}
	}
	if v.MistUpstream == "" {
		if v.Mode == "native" {
			v.MistUpstream = "localhost:8080"
		} else {
			v.MistUpstream = "mistserver:8080"
		}
	}
	if v.CaddyAdminAddr == "" {
		if v.Mode == "native" {
			v.CaddyAdminAddr = "localhost:2019"
		} else {
			v.CaddyAdminAddr = "unix//run/caddy/admin.sock"
		}
	}
	if v.ChandlerUpstream == "" {
		if v.Mode == "native" {
			v.ChandlerUpstream = "localhost:18020"
		} else {
			v.ChandlerUpstream = "chandler:18020"
		}
	}
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

	var out []EdgeRenderedFile

	out = append(out, EdgeRenderedFile{
		Path:      "maintenance.html",
		Content:   append([]byte(nil), maintenance.HTML...),
		Mode:      0o644,
		WriteMode: EdgeWriteIfMissingOrOverwrite,
	})

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
		vmagentConfig := fmt.Sprintf(`global:
  scrape_interval: 30s
scrape_configs:
  - job_name: edge-mist
    metrics_path: %s
    static_configs:
      - targets:
          - "mistserver:8080"
        labels:
          frameworks_mode: "edge"
          frameworks_node: %q
          frameworks_service: "mistserver"
  - job_name: edge-helmsman
    metrics_path: /metrics
    static_configs:
      - targets:
          - "helmsman:18007"
        labels:
          frameworks_mode: "edge"
          frameworks_node: %q
          frameworks_service: "helmsman"
`, mist.MetricsPath, vars.NodeID, vars.NodeID)
		out = append(out, EdgeRenderedFile{
			Path:      "vmagent-edge.yml",
			Content:   []byte(vmagentConfig),
			Mode:      0o644,
			WriteMode: EdgeWriteAlways,
		})
		vmagentServiceBlock = `  vmagent:
    image: victoriametrics/vmagent:v1.122.0
    container_name: frameworks-edge-vmagent
    command:
      - -httpListenAddr=:8430
      - -promscrape.config=/etc/frameworks/vmagent-edge.yml
      - -remoteWrite.url={{TELEMETRY_URL}}
      - -remoteWrite.bearerTokenFile=/etc/frameworks/telemetry/token
    volumes:
      - ./vmagent-edge.yml:/etc/frameworks/vmagent-edge.yml:ro
      - ./telemetry:/etc/frameworks/telemetry:ro
    restart: unless-stopped
`
	}

	tplFiles := []struct{ in, out string }{
		{"edge/Caddyfile.tmpl", "Caddyfile"},
		{"edge/.edge.env.tmpl", ".edge.env"},
	}
	if vars.Mode != "native" {
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
		content = strings.ReplaceAll(content, "{{MIST_API_PASSWORD}}", vars.MistAPIPassword)
		content = strings.ReplaceAll(content, "{{CHANDLER_UPSTREAM}}", vars.ChandlerUpstream)
		content = strings.ReplaceAll(content, "{{TELEMETRY_URL}}", vars.TelemetryURL)
		content = strings.ReplaceAll(content, "{{VMAGENT_EDGE_SERVICE}}", vmagentServiceBlock)
		content = strings.ReplaceAll(content, "{{TELEMETRY_TOKEN}}", vars.TelemetryToken)
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
