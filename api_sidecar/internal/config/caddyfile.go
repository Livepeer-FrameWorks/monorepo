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
	# The internal CA's root stays out of the host's system trust store.
	skip_install_trust
{{- with .ManagedNoSNIName}}
	# A TLS client that sends no SNI (a load-balancer health monitor dialing
	# the origin IP) gets the managed certificate of the edge domain's site.
	# Caddy looks this name up as an exact certificate cache key, which is the
	# site address the certificate was obtained for, wildcard included.
	default_sni {{.}}
{{- end}}
{{- if .AcmeEmail}}
	email {{.AcmeEmail}}
{{- end}}
	# Runtime/error logging is configured globally: a site-level log directive
	# only covers access logs, while upstream failures are emitted by the
	# default logger and carry the full request URI — which for WHIP publishes
	# (/webrtc/<streamKey>) and the ingest front door is a credential.
	log {
		output stdout
		format filter {
			wrap json
			fields {
				request>uri regexp "/(webrtc|ingest)/[^/?]+" "/${1}/REDACTED"
			}
		}
	}
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

	@edge_health {
		host {{.EdgeDomain}}
		path /health
	}
	handle @edge_health {
		reverse_proxy {{.HelmsmanUpstream}}
	}

	# Cross-cluster peer-relay reads of hot-but-unsynced artifacts.
	# Scoped to the per-node edge FQDN so the route is invisible on
	# tenant/customer hosts. Helmsman's middleware requires an opaque
	# peer-relay grant id (Authorization: Bearer) that Foghorn authorizes.
	@artifact_relay {
		host {{.EdgeDomain}}
		path /internal/artifact/*
	}
	handle @artifact_relay {
		reverse_proxy {{.HelmsmanUpstream}} {
			flush_interval -1
			transport http {
				read_timeout 0
				write_timeout 0
				keepalive 64s
			}
		}
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
		handle @view_options {
			respond "" 204
		}
		reverse_proxy {{.MistUpstream}} {
			header_down -Access-Control-Allow-Origin
			header_down -Access-Control-Allow-Methods
			header_down -Access-Control-Allow-Headers
			header_down -Access-Control-Expose-Headers
			header_down -Access-Control-Allow-Credentials
			header_down -Access-Control-Max-Age
			header_down -Access-Control-Request-Headers
			header_down -Access-Control-Request-Method
			flush_interval -1
			transport http {
				read_timeout 0
				write_timeout 0
				keepalive 64s
			}
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
		# WHIP publishes to /webrtc/<streamKey>, so the raw URI carries a
		# publishing credential; redact it rather than shipping it to logs.
		format filter {
			wrap json
			fields {
				request>uri regexp "/(webrtc|ingest)/[^/?]+" "/${1}/REDACTED"
			}
		}
	}
}
{{range .Bundles}}
{{.SiteAddress}} {
{{- if and .TLSCertPath .TLSKeyPath}}
	tls {{.TLSCertPath}} {{.TLSKeyPath}}
{{- end}}
	import common_handlers
}
{{end}}
{{- with .LoadedNoSNIBundle}}
# A TLS client that sends no SNI (a load-balancer health monitor dialing the
# origin IP) gets the edge domain's loaded certificate from this site. Caddy
# selects a loaded certificate only through its site's SNI-matched policy,
# which default_sni does not reach. The Host header still routes the request
# to the site serving that host; any other host gets 421.
https:// {
	tls {{.TLSCertPath}} {{.TLSKeyPath}}
	respond 421
}
{{end}}`

var parsedCaddyfileTmpl = template.Must(template.New("caddyfile").Parse(caddyfileTmpl))

type caddyfileTemplateData struct {
	CaddyfileParams
	// LoadedNoSNIBundle is the bundle with loaded certificate files that
	// serves EdgeDomain; it renders the catch-all site for SNI-less clients.
	LoadedNoSNIBundle *CaddyfileBundle
	// ManagedNoSNIName is the site address of the managed (ACME) bundle that
	// serves EdgeDomain; it renders the global default_sni.
	ManagedNoSNIName string
}

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
	data := caddyfileTemplateData{CaddyfileParams: params}
	if bundle, address, ok := bundleServingHost(params.Bundles, params.EdgeDomain); ok {
		if bundle.TLSCertPath != "" && bundle.TLSKeyPath != "" {
			data.LoadedNoSNIBundle = &bundle
		} else {
			data.ManagedNoSNIName = address
		}
	}
	var buf strings.Builder
	if err := parsedCaddyfileTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering Caddyfile: %w", err)
	}
	return buf.String(), nil
}

// bundleServingHost returns the bundle whose site serves host and the site
// address that matches it. An exact address wins over a wildcard, as it does
// in Caddy's own certificate selection.
func bundleServingHost(bundles []CaddyfileBundle, host string) (CaddyfileBundle, string, bool) {
	host = strings.Trim(strings.TrimSpace(host), ".")
	if host == "" {
		return CaddyfileBundle{}, "", false
	}
	for _, wildcard := range []bool{false, true} {
		for _, bundle := range bundles {
			for _, addr := range strings.Fields(bundle.SiteAddress) {
				if siteAddressMatchesHost(addr, host, wildcard) {
					return bundle, addr, true
				}
			}
		}
	}
	return CaddyfileBundle{}, "", false
}

// siteAddressMatchesHost reports whether addr is host itself or, when
// wildcard is set, a one-label wildcard over host.
func siteAddressMatchesHost(addr, host string, wildcard bool) bool {
	addr = strings.Trim(strings.TrimSpace(addr), ".")
	if !wildcard {
		return addr == host
	}
	suffix, ok := strings.CutPrefix(addr, "*.")
	if !ok {
		return false
	}
	label, ok := strings.CutSuffix(host, "."+suffix)
	return ok && label != "" && !strings.Contains(label, ".")
}
