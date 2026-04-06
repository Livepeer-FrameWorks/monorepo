package templates

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/pkg/maintenance"
)

//go:embed edge/*
var edgeFS embed.FS

type EdgeVars struct {
	NodeID          string
	EdgeDomain      string
	AcmeEmail       string
	FoghornHTTPBase string
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
	TelemetryUser    string
	TelemetryPass    string
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

// WriteEdgeTemplates writes edge stack templates into target directory.
// It will not overwrite existing files unless overwrite is true.
func WriteEdgeTemplates(targetDir string, vars EdgeVars, overwrite bool) error {
	vars.SetModeDefaults()

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	// Write shared maintenance page (embedded via pkg/maintenance)
	maintPath := filepath.Join(targetDir, "maintenance.html")
	if _, err := os.Stat(maintPath); err != nil || overwrite {
		if err := os.WriteFile(maintPath, maintenance.HTML, 0o644); err != nil {
			return err
		}
	}

	// files to render — native mode skips docker-compose
	files := []struct{ in, out string }{
		{"edge/Caddyfile.tmpl", "Caddyfile"},
		{"edge/.edge.env.tmpl", ".edge.env"},
	}
	if vars.Mode != "native" {
		files = append([]struct{ in, out string }{
			{"edge/docker-compose.edge.yml.tmpl", "docker-compose.edge.yml"},
		}, files...)
	}
	if strings.TrimSpace(vars.CABundlePEM) != "" {
		pkiDir := filepath.Join(targetDir, "pki")
		if err := os.MkdirAll(pkiDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(pkiDir, "ca.crt"), []byte(vars.CABundlePEM), 0o644); err != nil {
			return err
		}
	}
	for _, f := range files {
		b, err := edgeFS.ReadFile(f.in)
		if err != nil {
			return err
		}
		content := string(b)
		content = strings.ReplaceAll(content, "{{NODE_ID}}", vars.NodeID)
		content = strings.ReplaceAll(content, "{{EDGE_DOMAIN}}", vars.EdgeDomain)
		content = strings.ReplaceAll(content, "{{ACME_EMAIL}}", vars.AcmeEmail)
		content = strings.ReplaceAll(content, "{{FOGHORN_HTTP_BASE}}", vars.FoghornHTTPBase)
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
		content = strings.ReplaceAll(content, "{{TELEMETRY_USER}}", vars.TelemetryUser)
		content = strings.ReplaceAll(content, "{{TELEMETRY_PASS}}", vars.TelemetryPass)
		// TLS directive placeholder (kept for backward compat with any templates that reference it)
		if vars.CertPath != "" && vars.KeyPath != "" {
			tlsDirective := fmt.Sprintf("tls %s %s", vars.CertPath, vars.KeyPath)
			content = strings.ReplaceAll(content, "{{TLS_DIRECTIVE}}", tlsDirective)
		} else {
			content = strings.ReplaceAll(content, "{{TLS_DIRECTIVE}}", "")
		}
		outPath := filepath.Join(targetDir, f.out)
		if _, err := os.Stat(outPath); err == nil && !overwrite {
			return fmt.Errorf("file exists: %s (use overwrite)", outPath)
		}
		if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
