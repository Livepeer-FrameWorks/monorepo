package config

import (
	"strings"
	"testing"
)

func baseParams() CaddyfileParams {
	return CaddyfileParams{
		Bundles: []CaddyfileBundle{
			{
				SiteAddress: "*.media-us-1.frameworks.network",
				TLSCertPath: "/etc/frameworks/certs/bundles/cluster_media-us-1.crt",
				TLSKeyPath:  "/etc/frameworks/certs/bundles/cluster_media-us-1.key",
			},
			{
				SiteAddress: "acme.cdn.frameworks.network *.acme.cdn.frameworks.network",
				TLSCertPath: "/etc/frameworks/certs/bundles/tenant_acme.crt",
				TLSKeyPath:  "/etc/frameworks/certs/bundles/tenant_acme.key",
			},
		},
		CaddyAdminAddr:   "localhost:2019",
		HelmsmanUpstream: "localhost:18007",
		ChandlerUpstream: "chandler:18020",
		MistUpstream:     "mistserver:8080",
	}
}

func TestRenderCaddyfile_MistAdminRouteRenderedWhenEdgeDomainSet(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}

	if !strings.Contains(out, "@mist_admin {") {
		t.Errorf("expected @mist_admin matcher block; got:\n%s", out)
	}
	if !strings.Contains(out, "host edge-us-1.media-us-1.frameworks.network") {
		t.Errorf("expected host matcher pinned to the edge domain; got:\n%s", out)
	}
	if !strings.Contains(out, "path /_mist-session /_mist /_mist/*") {
		t.Errorf("expected path /_mist-session /_mist /_mist/* inside the matcher; got:\n%s", out)
	}
	if !strings.Contains(out, "handle @mist_admin {") {
		t.Errorf("expected handle @mist_admin block referencing the matcher; got:\n%s", out)
	}
}

func TestRenderCaddyfile_MistAdminRouteAbsentWhenEdgeDomainEmpty(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = ""

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	if strings.Contains(out, "_mist") {
		t.Errorf("did not expect any _mist tokens when EdgeDomain is empty; got:\n%s", out)
	}
	if strings.Contains(out, "@mist_admin") {
		t.Errorf("did not expect @mist_admin matcher when EdgeDomain is empty; got:\n%s", out)
	}
}

func TestRenderCaddyfile_MistAdminUsesHandleNotHandlePath(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}

	// Caddy must preserve the /_mist prefix — Helmsman strips it. Using
	// handle_path here would cause the prefix to be stripped at the Caddy
	// hop, which breaks the prefix-strip-and-forward contract Helmsman
	// owns and which Mist's relative-path LSP frontend depends on.
	if strings.Contains(out, "handle_path /_mist") {
		t.Errorf("must use 'handle', not 'handle_path', for /_mist; got:\n%s", out)
	}
}

func TestRenderCaddyfile_MistAdminRouteIsHostMatchedNotBare(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}

	// The route MUST be reached via the @mist_admin matcher (which carries
	// the host clause), never via a bare path handler that any wildcard
	// bundle host would match. A bare `handle /_mist*` in common_handlers
	// would silently inherit the admin surface onto every tenant /
	// customer site that imports common_handlers.
	matcherOccurrences := strings.Count(out, "@mist_admin")
	if matcherOccurrences < 2 {
		t.Fatalf("expected @mist_admin to appear in both matcher definition and handle; got %d occurrences", matcherOccurrences)
	}
	if strings.Count(out, "path /_mist-session /_mist /_mist/*") != 1 {
		t.Errorf("expected exactly one host-matched mist admin path matcher; rendered:\n%s", out)
	}
	for _, forbidden := range []string{"handle /_mist", "handle_path /_mist", "route /_mist"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("admin route must only use the host-matched @mist_admin matcher; found %q in:\n%s", forbidden, out)
		}
	}
}

func TestRenderCaddyfile_MistAdminReverseProxiesToHelmsman(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"
	p.HelmsmanUpstream = "127.0.0.1:18007"

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}

	// Sanity: the handle for @mist_admin proxies to Helmsman, not to Mist
	// (operators must hit the auth boundary, never go straight to Mist).
	handleStart := strings.Index(out, "handle @mist_admin {")
	if handleStart < 0 {
		t.Fatalf("missing handle @mist_admin block; got:\n%s", out)
	}
	handleEnd := strings.Index(out[handleStart:], "}")
	if handleEnd < 0 {
		t.Fatalf("unterminated handle @mist_admin block; got:\n%s", out)
	}
	body := out[handleStart : handleStart+handleEnd]
	if !strings.Contains(body, "reverse_proxy 127.0.0.1:18007") {
		t.Errorf("expected reverse_proxy to helmsman upstream inside handle; got body:\n%s", body)
	}
	if strings.Contains(body, "mistserver") {
		t.Errorf("admin handle must not proxy directly to mistserver; got body:\n%s", body)
	}
}

func TestRenderCaddyfile_ViewRouteStripsPrefixForMist(t *testing.T) {
	out, err := RenderCaddyfile(baseParams())
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}

	if !strings.Contains(out, "handle_path /view/* {") {
		t.Fatalf("expected /view route to strip prefix before proxying to Mist; got:\n%s", out)
	}
	if strings.Contains(out, "handle /view/* {") {
		t.Fatalf("must not preserve /view prefix when proxying to Mist; got:\n%s", out)
	}
	if !strings.Contains(out, "header_up X-Mst-Path {scheme}://{host}/view/") {
		t.Fatalf("expected X-Mst-Path public base header for Mist; got:\n%s", out)
	}
	for _, headerName := range []string{
		"Access-Control-Allow-Credentials",
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Origin",
		"Access-Control-Expose-Headers",
		"Access-Control-Max-Age",
		"Access-Control-Request-Headers",
		"Access-Control-Request-Method",
	} {
		if !strings.Contains(out, "header_down -"+headerName) {
			t.Fatalf("expected /view proxy to strip Mist upstream CORS header %s; got:\n%s", headerName, out)
		}
	}
}

func TestRenderCaddyfile_MediaRoutesExposeCorsHeaders(t *testing.T) {
	out, err := RenderCaddyfile(baseParams())
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}

	if count := strings.Count(out, "Access-Control-Allow-Origin \"*\""); count < 2 {
		t.Fatalf("expected CORS headers on /assets and /view routes, got %d occurrences:\n%s", count, out)
	}
	for _, want := range []string{"respond @assets_options \"\" 204", "respond @view_options \"\" 204"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected CORS preflight response %q; got:\n%s", want, out)
		}
	}
}
