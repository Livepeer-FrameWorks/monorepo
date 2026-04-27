package provisioner

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestReverseProxyComposeVarsRendersNginxConfigMount(t *testing.T) {
	vars, err := reverseProxyComposeVars("nginx", 18090, ServiceConfig{
		Mode:  "docker",
		Image: "nginx:alpine",
		Port:  18090,
		Metadata: map[string]any{"proxy_sites": []map[string]any{{
			"domains":  []string{"bridge.example.com"},
			"upstream": "127.0.0.1:18000",
		}}},
	})
	if err != nil {
		t.Fatalf("reverseProxyComposeVars: %v", err)
	}
	compose := vars["compose_stack_compose_content"].(string)
	if !strings.Contains(compose, `"18090:80"`) {
		t.Fatalf("compose did not map host port to container port 80:\n%s", compose)
	}
	files := vars["compose_stack_files"].(map[string]any)
	conf := files["frameworks.conf"].(string)
	for _, want := range []string{
		`extra_hosts:`,
		`"18090:80"`,
		"server_name bridge.example.com;",
		"proxy_pass http://host.docker.internal:18000;",
	} {
		if !strings.Contains(compose+conf, want) {
			t.Fatalf("docker nginx output missing %q:\ncompose:\n%s\nconfig:\n%s", want, compose, conf)
		}
	}
}

func TestReverseProxyComposeVarsRendersTLSMountsAndHTTPSPort(t *testing.T) {
	vars, err := reverseProxyComposeVars("nginx", 18090, ServiceConfig{
		Mode:  "docker",
		Image: "nginx:alpine",
		Port:  80,
		Metadata: map[string]any{"proxy_sites": []map[string]any{{
			"domains":       []string{"bridge.example.com"},
			"upstream":      "localhost:18000",
			"tls_cert_path": "/etc/frameworks/certs/bridge.crt",
			"tls_key_path":  "/etc/frameworks/certs/bridge.key",
		}}},
	})
	if err != nil {
		t.Fatalf("reverseProxyComposeVars: %v", err)
	}
	compose := vars["compose_stack_compose_content"].(string)
	files := vars["compose_stack_files"].(map[string]any)
	conf := files["frameworks.conf"].(string)
	for _, want := range []string{
		`"80:80"`,
		`"443:443"`,
		`/etc/frameworks/certs:/etc/frameworks/certs:ro`,
		`listen 443 ssl http2;`,
		`ssl_certificate /etc/frameworks/certs/bridge.crt;`,
		`proxy_pass http://host.docker.internal:18000;`,
	} {
		if !strings.Contains(conf, want) {
			if !strings.Contains(compose, want) {
				t.Fatalf("docker TLS output missing %q:\ncompose:\n%s\nconfig:\n%s", want, compose, conf)
			}
		}
	}
}

func TestNginxRoleVarsUsesProxySites(t *testing.T) {
	vars, err := nginxRoleVars(context.TODO(), nilHost(), ServiceConfig{
		Port: 18090,
		Metadata: map[string]any{"proxy_sites": []map[string]any{{
			"domains":  []string{"bridge.example.com"},
			"upstream": "127.0.0.1:18000",
		}}},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("nginxRoleVars: %v", err)
	}
	sites := vars["nginx_sites"].([]map[string]any)
	if len(sites) != 1 {
		t.Fatalf("nginx_sites len = %d, want 1", len(sites))
	}
	if vars["nginx_http_port"] != 18090 {
		t.Fatalf("nginx_http_port = %v", vars["nginx_http_port"])
	}
}

func TestProxySiteMapsPreserveHTTPSUpstreams(t *testing.T) {
	sites := proxySiteMapsForMode(map[string]any{"proxy_sites": []map[string]any{{
		"domains":  []string{"secure.example.com"},
		"upstream": "https://127.0.0.1:18443",
	}}}, "docker")
	if len(sites) != 1 {
		t.Fatalf("sites len = %d, want 1", len(sites))
	}
	if got := sites[0]["upstream"]; got != "https://host.docker.internal:18443" {
		t.Fatalf("upstream = %v", got)
	}
	if got := sites[0]["proxy_pass"]; got != "https://host.docker.internal:18443" {
		t.Fatalf("proxy_pass = %v", got)
	}
}

func TestRenderCaddyfileSupportsTLSAndPathPrefixes(t *testing.T) {
	content := renderCaddyfile([]proxySite{{
		Domains:      []string{"bridge.example.com"},
		Upstream:     "127.0.0.1:18000",
		PathPrefixes: []string{"/graphql"},
		TLSMode:      "internal",
	}})
	for _, want := range []string{
		"bridge.example.com {",
		"tls internal",
		"reverse_proxy /graphql 127.0.0.1:18000",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("Caddyfile missing %q:\n%s", want, content)
		}
	}
}

func nilHost() inventory.Host {
	return inventory.Host{}
}
