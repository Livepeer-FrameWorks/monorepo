package templates

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed edge/*
var edgeFS embed.FS

type EdgeVars struct {
	EdgeDomain      string
	AcmeEmail       string
	FoghornHTTPBase string
	FoghornGRPCAddr string
	EnrollmentToken string
	// Optional: file-based TLS certificate paths (if using Navigator-issued certs)
	CertPath string // e.g., /etc/frameworks/certs/cert.pem
	KeyPath  string // e.g., /etc/frameworks/certs/key.pem
}

// WriteEdgeTemplates writes edge stack templates into target directory.
// It will not overwrite existing files unless overwrite is true.
func WriteEdgeTemplates(targetDir string, vars EdgeVars, overwrite bool) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	// files to render
	files := []struct{ in, out string }{
		{"edge/docker-compose.edge.yml.tmpl", "docker-compose.edge.yml"},
		{"edge/Caddyfile.tmpl", "Caddyfile"},
		{"edge/.edge.env.tmpl", ".edge.env"},
	}
	for _, f := range files {
		b, err := edgeFS.ReadFile(f.in)
		if err != nil {
			return err
		}
		content := string(b)
		content = strings.ReplaceAll(content, "{{EDGE_DOMAIN}}", vars.EdgeDomain)
		content = strings.ReplaceAll(content, "{{ACME_EMAIL}}", vars.AcmeEmail)
		content = strings.ReplaceAll(content, "{{FOGHORN_HTTP_BASE}}", vars.FoghornHTTPBase)
		content = strings.ReplaceAll(content, "{{FOGHORN_GRPC_ADDR}}", vars.FoghornGRPCAddr)
		content = strings.ReplaceAll(content, "{{ENROLLMENT_TOKEN}}", vars.EnrollmentToken)
		content = strings.ReplaceAll(content, "{{CERT_PATH}}", vars.CertPath)
		content = strings.ReplaceAll(content, "{{KEY_PATH}}", vars.KeyPath)
		// TLS: use explicit cert paths if provided, otherwise auto-ACME (Caddy default).
		// ConfigSeed will push wildcard certs to /etc/frameworks/certs/ at runtime;
		// Caddy watches the files and hot-reloads when they appear.
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
