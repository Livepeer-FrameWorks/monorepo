package config

import (
	"strings"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
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

func TestRenderCaddyfile_RedactsPublishingCredentialsFromAllLogs(t *testing.T) {
	out, err := RenderCaddyfile(baseParams())
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}

	const filter = `request>uri regexp "/(webrtc|ingest)/[^/?]+" "/${1}/REDACTED"`
	// The global logger covers runtime and upstream-error events. The site logger covers access
	// events. Both must apply the same filter because either event class can carry the publishing
	// credential in its request URI.
	if got := strings.Count(out, filter); got != 2 {
		t.Fatalf("publishing credential filter count = %d, want 2; rendered:\n%s", got, out)
	}
	if strings.Contains(out, "format json") {
		t.Fatalf("unfiltered JSON logger can expose publishing credentials; rendered:\n%s", out)
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

func TestRenderCaddyfile_EdgeHealthRouteIsHostMatched(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"
	p.HelmsmanUpstream = "127.0.0.1:18007"

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	if !strings.Contains(out, "@edge_health {") {
		t.Fatalf("expected @edge_health matcher; got:\n%s", out)
	}
	if !strings.Contains(out, "host edge-us-1.media-us-1.frameworks.network") {
		t.Fatalf("expected edge health matcher pinned to edge host; got:\n%s", out)
	}
	if !strings.Contains(out, "path /health") {
		t.Fatalf("expected /health path matcher; got:\n%s", out)
	}
	handleStart := strings.Index(out, "handle @edge_health {")
	if handleStart < 0 {
		t.Fatalf("missing handle @edge_health block; got:\n%s", out)
	}
	handleEnd := strings.Index(out[handleStart:], "}")
	if handleEnd < 0 {
		t.Fatalf("unterminated handle @edge_health block; got:\n%s", out)
	}
	body := out[handleStart : handleStart+handleEnd]
	if !strings.Contains(body, "reverse_proxy 127.0.0.1:18007") {
		t.Fatalf("edge health must proxy to Helmsman; got body:\n%s", body)
	}
	if strings.Contains(out, "handle /health") || strings.Contains(out, "handle_path /health") {
		t.Fatalf("/health must be host-matched through @edge_health; got:\n%s", out)
	}
}

// noSNISite returns the body of the rendered catch-all "https://" site, or
// false when none is rendered.
func noSNISite(t *testing.T, caddyfile string) (string, bool) {
	t.Helper()
	if strings.Count(caddyfile, "\nhttps:// {") > 1 {
		t.Fatalf("more than one catch-all site rendered:\n%s", caddyfile)
	}
	_, block, ok := strings.Cut(caddyfile, "\nhttps:// {\n")
	if !ok {
		return "", false
	}
	block, _, _ = strings.Cut(block, "\n}\n")
	return block, true
}

func globalOptions(caddyfile string) string {
	global, _, _ := strings.Cut(caddyfile, "\n}\n")
	return global
}

// Navigator's Cloudflare monitor dials the origin IP over TLS without SNI.
// Caddy picks a loaded certificate only through the SNI-matched policy of its
// site, so the edge's loaded certificate must also be loaded by a catch-all
// site; default_sni would never reach it. TestEdgeCaddyfile_NoSNIHandshake_RealCaddy
// (caddy_verify) proves the handshake against real Caddy.
func TestRenderCaddyfile_NoSNIClientGetsLoadedEdgeCertificate(t *testing.T) {
	p := baseParams()
	// The bundle serving the edge domain is not the first one.
	p.Bundles[0], p.Bundles[1] = p.Bundles[1], p.Bundles[0]
	p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	site, ok := noSNISite(t, out)
	if !ok {
		t.Fatalf("no catch-all site for SNI-less clients:\n%s", out)
	}
	wantTLS := "tls /etc/frameworks/certs/bundles/cluster_media-us-1.crt /etc/frameworks/certs/bundles/cluster_media-us-1.key"
	var lines []string
	for line := range strings.SplitSeq(site, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 2 || lines[0] != wantTLS || lines[1] != "respond 421" {
		t.Fatalf("catch-all site = %q, want [%q \"respond 421\"]", lines, wantTLS)
	}
	if strings.Contains(globalOptions(out), "default_sni") {
		t.Fatalf("default_sni cannot select a loaded certificate and must not be rendered:\n%s", globalOptions(out))
	}
}

// An exact site address for the edge domain wins over a wildcard covering it,
// as Caddy's own certificate selection does.
func TestRenderCaddyfile_NoSNIPrefersExactEdgeDomainBundle(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"
	p.Bundles = append(p.Bundles, CaddyfileBundle{
		SiteAddress: p.EdgeDomain,
		TLSCertPath: "/etc/frameworks/certs/bundles/edge.crt",
		TLSKeyPath:  "/etc/frameworks/certs/bundles/edge.key",
	})

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	site, ok := noSNISite(t, out)
	if !ok || !strings.Contains(site, "tls /etc/frameworks/certs/bundles/edge.crt /etc/frameworks/certs/bundles/edge.key") {
		t.Fatalf("catch-all site does not load the exact edge bundle:\n%s", out)
	}
}

// A managed certificate is cached under the site address it was obtained for,
// and default_sni is an exact cache lookup, so it names that address. A
// catch-all site could never obtain a certificate of its own.
func TestRenderCaddyfile_NoSNIClientGetsManagedEdgeCertificate(t *testing.T) {
	for _, tc := range []struct {
		name, siteAddress string
	}{
		{name: "edge domain site", siteAddress: "edge-us-1.media-us-1.frameworks.network"},
		{name: "wildcard site", siteAddress: "*.media-us-1.frameworks.network"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := baseParams()
			p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"
			p.Bundles = []CaddyfileBundle{p.Bundles[1], {SiteAddress: tc.siteAddress}}

			out, err := RenderCaddyfile(p)
			if err != nil {
				t.Fatalf("RenderCaddyfile: %v", err)
			}
			if _, ok := noSNISite(t, out); ok {
				t.Fatalf("catch-all site rendered for a managed certificate:\n%s", out)
			}
			global := globalOptions(out)
			if got := strings.Count(global, "default_sni"); got != 1 || !strings.Contains(global, "\tdefault_sni "+tc.siteAddress+"\n") {
				t.Fatalf("global options want default_sni %s once; got:\n%s", tc.siteAddress, global)
			}
		})
	}
}

func TestRenderCaddyfile_NoSNIUnchangedWhenNoBundleServesEdgeDomain(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = "edge-us-1.media-eu-1.frameworks.network"

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	if _, ok := noSNISite(t, out); ok || strings.Contains(globalOptions(out), "default_sni") {
		t.Fatalf("no bundle serves the edge domain, yet an SNI-less fallback is rendered:\n%s", out)
	}
}

func TestRenderCaddyfile_EdgeHealthAbsentWhenEdgeDomainEmpty(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = ""

	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	if strings.Contains(out, "edge_health") || strings.Contains(out, "path /health") {
		t.Fatalf("did not expect edge health route without EdgeDomain; got:\n%s", out)
	}
	if _, ok := noSNISite(t, out); ok || strings.Contains(globalOptions(out), "default_sni") {
		t.Fatalf("did not expect an SNI-less fallback without EdgeDomain; got:\n%s", out)
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
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Origin",
		"Access-Control-Expose-Headers",
		"Access-Control-Allow-Credentials",
		"Access-Control-Max-Age",
		"Access-Control-Request-Headers",
		"Access-Control-Request-Method",
	} {
		if !strings.Contains(out, "header_down -"+headerName) {
			t.Fatalf("expected /view proxy to strip Mist upstream CORS header %s; got:\n%s", headerName, out)
		}
	}
	for _, headerName := range []string{
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Origin",
		"Access-Control-Expose-Headers",
	} {
		if !strings.Contains(out, "\t\t\t"+headerName) {
			t.Fatalf("expected /view route to own CORS response header %s; got:\n%s", headerName, out)
		}
		if strings.Contains(out, "header_down "+headerName) {
			t.Fatalf("expected /view proxy to set CORS outside header_down for %s; got:\n%s", headerName, out)
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
	for _, want := range []string{"respond @assets_options \"\" 204", "respond \"\" 204"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected CORS preflight response %q; got:\n%s", want, out)
		}
	}
}

// Caddy's /load warns "Caddyfile input is not formatted" when the input
// differs from caddy fmt output, which collapses runs of blank lines.
func TestRenderCaddyfileHasNoRepeatedBlankLines(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"
	p.AcmeEmail = "ops@frameworks.network"
	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	if index := strings.Index(out, "\n\n\n"); index >= 0 {
		line := strings.Count(out[:index], "\n") + 2
		t.Fatalf("repeated blank lines at line %d:\n%s", line, out)
	}
	for _, bundle := range p.Bundles {
		if !strings.Contains(out, "}\n\n"+bundle.SiteAddress+" {") {
			t.Fatalf("site block %q is not separated by one blank line:\n%s", bundle.SiteAddress, out)
		}
	}
}

// The edge's Caddy can serve with its internal CA; that CA's root must never be
// installed into the host's system trust store.
func TestRenderCaddyfile_KeepsInternalCAOutOfSystemTrust(t *testing.T) {
	out, err := RenderCaddyfile(baseParams())
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	global := out
	if end := strings.Index(out, "\n}\n"); end >= 0 {
		global = out[:end]
	}
	if !strings.Contains(global, "skip_install_trust") {
		t.Fatalf("global options do not set skip_install_trust:\n%s", global)
	}
}

// artifactRelayHosts returns the host matcher of the rendered @artifact_relay
// route, the only route that hands /internal/artifact/* to Helmsman.
func artifactRelayHosts(t *testing.T, caddyfile string) []string {
	t.Helper()
	_, block, ok := strings.Cut(caddyfile, "@artifact_relay {")
	if !ok {
		t.Fatalf("no @artifact_relay route rendered:\n%s", caddyfile)
	}
	block, _, _ = strings.Cut(block, "}")
	for line := range strings.SplitSeq(block, "\n") {
		if hosts, found := strings.CutPrefix(strings.TrimSpace(line), "host "); found {
			return strings.Fields(hosts)
		}
	}
	t.Fatalf("@artifact_relay has no host matcher:\n%s", block)
	return nil
}

// Foghorn addresses peer relay reads to the ConfigSeed's Site.EdgeDomain, so
// the Caddyfile rendered from that seed must route /internal/artifact/* for
// exactly that host and not for the EDGE_PUBLIC_URL playback host the node
// advertises as its base URL.
func TestCaddyfileForSeedRoutesArtifactRelayOnSeededEdgeDomainOnly(t *testing.T) {
	const edgeDomain = "edge-fw-stg-edge-eu.staging-media-eu.example.com"
	seed := &ipcpb.ConfigSeed{
		NodeId: "fw-stg-edge-eu",
		Site: &ipcpb.SiteConfig{
			SiteAddress: "*.staging-media-eu.example.com",
			EdgeDomain:  edgeDomain,
			PoolDomain:  "edge.staging-media-eu.example.com",
		},
		TlsBundles: []*ipcpb.TLSCertBundle{{
			BundleId:      "cluster_staging-media-eu",
			SiteAddresses: []string{"*.staging-media-eu.example.com"},
		}},
	}
	out, err := RenderCaddyfile(caddyfileParamsForSeed(seed, composeCaddyBundles(seed)))
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	hosts := artifactRelayHosts(t, out)
	if len(hosts) != 1 || hosts[0] != edgeDomain {
		t.Fatalf("@artifact_relay hosts = %v, want exactly [%s]", hosts, edgeDomain)
	}
}

// Caddy's reverse_proxy already sets X-Forwarded-For to the client address and
// X-Forwarded-Proto to the scheme when no trusted_proxies are configured, and
// warns on every config load about explicit copies of those defaults.
func TestRenderCaddyfile_NoRedundantForwardedHeaders(t *testing.T) {
	p := baseParams()
	p.EdgeDomain = "edge-us-1.media-us-1.frameworks.network"
	out, err := RenderCaddyfile(p)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	for _, line := range []string{"header_up X-Forwarded-Proto", "header_up X-Forwarded-For"} {
		if strings.Contains(out, line) {
			t.Fatalf("rendered Caddyfile repeats a reverse_proxy default %q:\n%s", line, out)
		}
	}
	if strings.Contains(out, "trusted_proxies") {
		t.Fatal("trusted_proxies changes the forwarded-header defaults this route relies on")
	}
}
