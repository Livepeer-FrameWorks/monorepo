package config

import (
	"fmt"
	"strings"
	"text/template"
)

// CaddyfileBundle describes one TLS site block to render. Each bundle gets
// its own site block with its own cert/key paths; the body of the block is
// shared via the `common_handlers` snippet so behavior stays identical
// across bundles.
type CaddyfileBundle struct {
	// SiteAddress can be one host or a space-separated list of hosts.
	// Examples:
	//   "*.media-us-1.frameworks.network"
	//   "acme.cdn.frameworks.network *.acme.cdn.frameworks.network"
	SiteAddress string
	// Empty paths leave the site to Caddy's automatic ACME (rare; we
	// always populate these from ConfigSeed in production).
	TLSCertPath string
	TLSKeyPath  string
}

// CaddyfileParams holds the values needed to render a production Caddyfile.
type CaddyfileParams struct {
	// One site block is rendered per bundle. Order is deterministic.
	Bundles          []CaddyfileBundle
	CaddyAdminAddr   string // e.g. "unix//run/caddy/admin.sock" or "localhost:2019"
	AcmeEmail        string
	HelmsmanUpstream string // e.g. "helmsman:18007" or "localhost:18007"
	ChandlerUpstream string // e.g. "chandler:18020" or "localhost:18020"
	MistUpstream     string // e.g. "mistserver:8080" or "localhost:8080"
	// EdgeDomain scopes the operator-only /_mist admin route to this exact
	// host. Empty means the admin route is not rendered (the proxy stays
	// off; tenant/customer hosts importing common_handlers cannot match).
	EdgeDomain string
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

(common_handlers) {
	@compressible {
		not path *.ts *.m4s *.mp4 *.webm
	}
	encode @compressible zstd gzip

	header {
		Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"
		X-Content-Type-Options "nosniff"
		X-Frame-Options "SAMEORIGIN"
		Referrer-Policy "strict-origin-when-cross-origin"
		Permissions-Policy "geolocation=(), microphone=()"
	}

	handle_path /webhooks/* {
		reverse_proxy {{.HelmsmanUpstream}}
	}
{{- if .EdgeDomain}}

	@mist_admin {
		host {{.EdgeDomain}}
		path /_mist-session /_mist /_mist/*
	}
	handle @mist_admin {
		reverse_proxy {{.HelmsmanUpstream}}
	}
{{- end}}

	handle /assets/* {
		header {
			Access-Control-Allow-Origin "*"
			Access-Control-Allow-Methods "GET, HEAD, OPTIONS"
			Access-Control-Allow-Headers "DNT,User-Agent,X-Requested-With,If-Modified-Since,Cache-Control,Content-Type,Range"
			Access-Control-Expose-Headers "Content-Length,Content-Range,Accept-Ranges"
		}
		@assets_options method OPTIONS
		respond @assets_options "" 204
		reverse_proxy {{.ChandlerUpstream}}
	}

	handle_path /view/* {
		header {
			Access-Control-Allow-Origin "*"
			Access-Control-Allow-Methods "GET, HEAD, OPTIONS"
			Access-Control-Allow-Headers "DNT,User-Agent,X-Requested-With,If-Modified-Since,Cache-Control,Content-Type,Range"
			Access-Control-Expose-Headers "Content-Length,Content-Range,Accept-Ranges"
		}
		@view_options method OPTIONS
		respond @view_options "" 204
		reverse_proxy {{.MistUpstream}} {
			flush_interval -1
			transport http {
				read_timeout 0
				write_timeout 0
				keepalive 64s
			}
			header_up X-Forwarded-Proto {scheme}
			header_up X-Forwarded-For {remote_host}
			header_up X-Mst-Path {scheme}://{host}/view/
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

{{range .Bundles}}
{{.SiteAddress}} {
{{- if and .TLSCertPath .TLSKeyPath}}
	tls {{.TLSCertPath}} {{.TLSKeyPath}}
{{- end}}
	import common_handlers
}
{{end}}`

var parsedCaddyfileTmpl = template.Must(template.New("caddyfile").Parse(caddyfileTmpl))

// RenderCaddyfile renders a production Caddyfile from the given parameters.
// Requires at least one bundle.
func RenderCaddyfile(params CaddyfileParams) (string, error) {
	if len(params.Bundles) == 0 {
		return "", fmt.Errorf("at least one bundle is required")
	}
	for i, b := range params.Bundles {
		if b.SiteAddress == "" {
			return "", fmt.Errorf("bundle[%d]: SiteAddress is required", i)
		}
	}
	var buf strings.Builder
	if err := parsedCaddyfileTmpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("rendering Caddyfile: %w", err)
	}
	return buf.String(), nil
}
