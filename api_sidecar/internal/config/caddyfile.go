package config

import (
	"fmt"
	"strings"
	"text/template"
)

// CaddyfileParams holds the values needed to render a production Caddyfile.
type CaddyfileParams struct {
	SiteAddress      string // e.g. "*.us-west-1.frameworks.network"
	TLSCertPath      string // e.g. "/etc/frameworks/certs/cert.pem" (empty = auto-ACME)
	TLSKeyPath       string
	CaddyAdminAddr   string // e.g. "unix//run/caddy/admin.sock" or "localhost:2019"
	AcmeEmail        string
	HelmsmanUpstream string // e.g. "helmsman:18007" or "localhost:18007"
	ChandlerUpstream string // e.g. "chandler:18020" or "localhost:18020"
	MistUpstream     string // e.g. "mistserver:8080" or "localhost:8080"
}

const caddyfileTmpl = `{
  admin {{.CaddyAdminAddr}}
{{- if .AcmeEmail}}
  email {{.AcmeEmail}}
{{- end}}
  servers {
    protocols h1 h2 h3
  }
}

{{.SiteAddress}} {
{{- if and .TLSCertPath .TLSKeyPath}}
  tls {{.TLSCertPath}} {{.TLSKeyPath}}
{{- end}}

  @compressible {
    not path *.ts *.m4s *.mp4 *.webm
  }
  encode @compressible zstd gzip

  headers {
    Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"
    X-Content-Type-Options "nosniff"
    X-Frame-Options "SAMEORIGIN"
    Referrer-Policy "strict-origin-when-cross-origin"
    Permissions-Policy "geolocation=(), microphone=()"
  }

  handle_path /webhooks/* {
    reverse_proxy {{.HelmsmanUpstream}}
  }

  handle /assets/* {
    reverse_proxy {{.ChandlerUpstream}}
  }

  handle /view/* {
    reverse_proxy {{.MistUpstream}} {
      flush_interval -1
      transport http {
        read_timeout 0
        write_timeout 0
        keepalive 64
      }
      header_up X-Forwarded-Proto {scheme}
      header_up X-Forwarded-For {remote_host}
    }
  }

  reverse_proxy {{.MistUpstream}}

  handle_errors {
    @upstream_down expression ` + "`" + `{err.status_code} in [502, 503, 504]` + "`" + `
    handle @upstream_down {
      root * /etc/caddy
      rewrite * /maintenance.html
      file_server
    }
  }

  log {
    output stdout
    format json
  }
}
`

var parsedCaddyfileTmpl = template.Must(template.New("caddyfile").Parse(caddyfileTmpl))

// RenderCaddyfile renders a production Caddyfile from the given parameters.
func RenderCaddyfile(params CaddyfileParams) (string, error) {
	if params.SiteAddress == "" {
		return "", fmt.Errorf("SiteAddress is required")
	}
	var buf strings.Builder
	if err := parsedCaddyfileTmpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("rendering Caddyfile: %w", err)
	}
	return buf.String(), nil
}
