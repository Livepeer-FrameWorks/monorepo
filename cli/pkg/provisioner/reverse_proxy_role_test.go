package provisioner

import (
	"context"
	"os"
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
	rootConf := files["nginx.conf"].(string)
	conf := files["frameworks.conf"].(string)
	for _, want := range []string{
		`extra_hosts:`,
		`"18090:80"`,
		`ulimits:`,
		`./nginx.conf:/etc/nginx/nginx.conf:ro`,
		"server_name bridge.example.com;",
		"proxy_pass http://host.docker.internal:18000;",
		"include /etc/nginx/conf.d/frameworks.conf;",
		"client_max_body_size 16m;",
		"client_body_timeout 60s;",
		"send_timeout 60s;",
		"proxy_request_buffering on;",
		"proxy_buffering on;",
		"proxy_set_header Upgrade $http_upgrade;",
		"proxy_read_timeout 300s;",
		"proxy_send_timeout 300s;",
		"worker_processes auto;",
		"worker_connections 16384;",
	} {
		if !strings.Contains(compose+conf+rootConf, want) {
			t.Fatalf("docker nginx output missing %q:\ncompose:\n%s\nroot:\n%s\nconfig:\n%s", want, compose, rootConf, conf)
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
		`listen 443 ssl;`,
		`http2 on;`,
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

func TestReverseProxyComposeVarsAppliesMediaIngestProfile(t *testing.T) {
	vars, err := reverseProxyComposeVars("nginx", 18090, ServiceConfig{
		Mode:  "docker",
		Image: "nginx:alpine",
		Port:  18090,
		Metadata: map[string]any{"proxy_sites": []map[string]any{{
			"domains":  []string{"livepeer.example.com"},
			"upstream": "127.0.0.1:18060",
			"profile":  "media_ingest",
		}}},
	})
	if err != nil {
		t.Fatalf("reverseProxyComposeVars: %v", err)
	}
	files := vars["compose_stack_files"].(map[string]any)
	conf := files["frameworks.conf"].(string)
	for _, want := range []string{
		"client_max_body_size 512m;",
		"client_body_timeout 900s;",
		"send_timeout 900s;",
		"proxy_request_buffering off;",
		"proxy_buffering off;",
		"proxy_read_timeout 900s;",
		"proxy_send_timeout 900s;",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("docker nginx media ingest config missing %q:\n%s", want, conf)
		}
	}
}

func TestNginxRoleVarsUsesProxySites(t *testing.T) {
	vars, err := nginxRoleVars(context.TODO(), nilHost(), ServiceConfig{
		Port: 18090,
		Metadata: map[string]any{"proxy_sites": []map[string]any{{
			"domains":       []string{"bridge.example.com"},
			"upstream":      "127.0.0.1:18000",
			"profile":       "api",
			"tls_bundle_id": "bridge-cert",
			"tls_cert_path": "/etc/frameworks/ingress/tls/bridge-cert/tls.crt",
			"tls_key_path":  "/etc/frameworks/ingress/tls/bridge-cert/tls.key",
			"tls_mode":      "files",
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
	if sites[0]["profile"] != "api" {
		t.Fatalf("profile = %v", sites[0]["profile"])
	}
	if sites[0]["tls_bundle_id"] != "bridge-cert" {
		t.Fatalf("tls_bundle_id = %v", sites[0]["tls_bundle_id"])
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

func TestNativeNginxTemplatesOwnRootConfigAndRouteProfiles(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/nginx/templates/frameworks.conf.j2")
	for _, want := range []string{
		"nginx_route_profiles",
		"client_max_body_size {{ site.client_max_body_size | default(profile.client_max_body_size) }};",
		"client_body_timeout {{ site.client_body_timeout | default(profile.client_body_timeout) }};",
		"send_timeout {{ site.send_timeout | default(profile.send_timeout) }};",
		"proxy_request_buffering {{ 'on' if site.proxy_request_buffering | default(profile.proxy_request_buffering) else 'off' }};",
		"proxy_buffering {{ 'on' if site.proxy_buffering | default(profile.proxy_buffering) else 'off' }};",
		"proxy_set_header Upgrade $http_upgrade;",
		"proxy_set_header Connection \"upgrade\";",
		"nginx_effective_http2_directive_mode == 'listen_parameter'",
		"nginx_effective_http2_directive_mode == 'standalone'",
		"proxy_read_timeout {{ site.proxy_read_timeout | default(profile.proxy_read_timeout) }};",
		"proxy_send_timeout {{ site.proxy_send_timeout | default(profile.proxy_send_timeout) }};",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("native nginx template missing %q:\n%s", want, content)
		}
	}
	root := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/nginx/templates/nginx.conf.j2")
	for _, want := range []string{
		"worker_processes {{ nginx_worker_processes }};",
		"worker_rlimit_nofile {{ nginx_worker_rlimit_nofile }};",
		"worker_connections {{ nginx_worker_connections }};",
		"server_names_hash_bucket_size {{ nginx_server_names_hash_bucket_size }};",
		"types_hash_max_size {{ nginx_types_hash_max_size }};",
		"types_hash_bucket_size {{ nginx_types_hash_bucket_size }};",
		"include {{ nginx_effective_http_include_path }};",
	} {
		if !strings.Contains(root, want) {
			t.Fatalf("native nginx root template missing %q:\n%s", want, root)
		}
	}
	systemd := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/nginx/templates/nginx-systemd-override.conf.j2")
	if !strings.Contains(systemd, "LimitNOFILE={{ nginx_systemd_limit_nofile }}") {
		t.Fatalf("native nginx systemd override missing LimitNOFILE:\n%s", systemd)
	}
}

func TestChatwootComposeTemplateConsumesEnvFile(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/chatwoot/templates/compose.yml.j2")
	if got := strings.Count(content, "env_file:"); got != 2 {
		t.Fatalf("chatwoot compose env_file count = %d, want 2:\n%s", got, content)
	}
	if got := strings.Count(content, "- .env"); got != 2 {
		t.Fatalf("chatwoot compose .env count = %d, want 2:\n%s", got, content)
	}
	for _, want := range []string{
		"host.docker.internal:host-gateway",
		"condition: service_healthy",
		"set -e",
		"exec bundle exec rails s",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("chatwoot compose template missing %q:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{
		"chatwoot-postgres",
		"postgres:16-alpine",
		"chatwoot-postgres-data",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("chatwoot compose template must not include hidden Postgres sidecar %q:\n%s", forbidden, content)
		}
	}
}

func TestListmonkComposeTemplateConsumesEnvFile(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/listmonk/templates/compose.yml.j2")
	if got := strings.Count(content, "env_file:"); got != 1 {
		t.Fatalf("listmonk compose env_file count = %d, want 1:\n%s", got, content)
	}
	if got := strings.Count(content, "- .env"); got != 1 {
		t.Fatalf("listmonk compose .env count = %d, want 1:\n%s", got, content)
	}
	if !strings.Contains(content, "host.docker.internal:host-gateway") {
		t.Fatalf("listmonk compose template missing host-gateway for native colocated Postgres:\n%s", content)
	}
}

func TestSpecialComposeRoleEntrypointsAreTaggedForCLIProvision(t *testing.T) {
	for _, path := range []string{
		"ansible/collections/ansible_collections/frameworks/infra/roles/chatwoot/tasks/main.yml",
		"ansible/collections/ansible_collections/frameworks/infra/roles/listmonk/tasks/main.yml",
	} {
		content := readRepoFile(t, path)
		if !strings.Contains(content, "name: frameworks.infra.compose_stack") {
			t.Fatalf("%s does not delegate to compose_stack:\n%s", path, content)
		}
		if !strings.Contains(content, "tags: [install, configure, service, validate]") {
			t.Fatalf("%s compose_stack lifecycle include lacks CLI provision tags:\n%s", path, content)
		}
	}
}

func TestSpecialComposeRolesSetSlowFirstBootWaits(t *testing.T) {
	composeInstall := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/compose_stack/tasks/install.yml")
	if !strings.Contains(composeInstall, "wait_timeout: \"{{ compose_stack_wait_timeout }}\"") {
		t.Fatalf("compose_stack apply task does not pass wait_timeout:\n%s", composeInstall)
	}

	for _, tc := range []struct {
		name       string
		roleDir    string
		waitVar    string
		startAfter string
	}{
		{name: "chatwoot", roleDir: "chatwoot", waitVar: "chatwoot_wait_timeout: 600", startAfter: "start_period: 300s"},
		{name: "listmonk", roleDir: "listmonk", waitVar: "listmonk_wait_timeout: 300", startAfter: "start_period: 120s"},
	} {
		defaults := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/"+tc.roleDir+"/defaults/main.yml")
		if !strings.Contains(defaults, tc.waitVar) {
			t.Fatalf("%s defaults missing %q:\n%s", tc.name, tc.waitVar, defaults)
		}
		tasks := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/"+tc.roleDir+"/tasks/main.yml")
		if !strings.Contains(tasks, "compose_stack_wait_timeout: \"{{ "+tc.roleDir+"_wait_timeout }}\"") {
			t.Fatalf("%s tasks do not forward compose_stack_wait_timeout:\n%s", tc.name, tasks)
		}
		compose := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/"+tc.roleDir+"/templates/compose.yml.j2")
		if !strings.Contains(compose, tc.startAfter) || !strings.Contains(compose, "retries: 10") {
			t.Fatalf("%s compose healthcheck is too short for first boot:\n%s", tc.name, compose)
		}
	}
}

func TestPrivateerRoleLetsRuntimeRefreshPKIAndBootstrapUnhealthy(t *testing.T) {
	pki := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/privateer/tasks/pki.yml")
	for _, want := range []string{
		"path: /etc/frameworks",
		`mode: "0755"`,
		"ensure runtime service certificate directory",
		"+ '/services'",
		`owner: "{{ privateer_user }}"`,
		`group: "{{ privateer_group }}"`,
	} {
		if !strings.Contains(pki, want) {
			t.Fatalf("privateer PKI tasks should keep /etc/frameworks traversable and runtime PKI writable; missing %q:\n%s", want, pki)
		}
	}
	for _, want := range []string{
		`owner: "{{ privateer_user }}"`,
		`group: "{{ privateer_group }}"`,
	} {
		if strings.Count(pki, want) < 3 {
			t.Fatalf("privateer PKI tasks should make PKI directory, CA bundle, and service cert root privateer-owned; missing %q:\n%s", want, pki)
		}
	}

	validate := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/privateer/tasks/validate.yml")
	for _, want := range []string{
		"status_code: [200, 503]",
		"getent hosts quartermaster.internal",
	} {
		if !strings.Contains(validate, want) {
			t.Fatalf("privateer validate task missing %q:\n%s", want, validate)
		}
	}
}

func TestSharedFrameworksPKIRolesPreservePrivateerAccess(t *testing.T) {
	goServicePKI := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/go_service/tasks/pki.yml")
	caDirTask := goServicePKI[:strings.Index(goServicePKI, "- name: Render internal CA trust bundle")]
	for _, forbidden := range []string{
		"owner: root",
		"group: root",
	} {
		if strings.Contains(caDirTask, forbidden) {
			t.Fatalf("go_service CA directory task must preserve existing shared PKI ownership; found %q:\n%s", forbidden, caDirTask)
		}
	}

	chatwootSecret := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/chatwoot/tasks/secret.yml")
	frameworksDirTask := chatwootSecret[:strings.Index(chatwootSecret, "- name: Check for an existing Chatwoot secret key")]
	if !strings.Contains(frameworksDirTask, `mode: "0755"`) {
		t.Fatalf("chatwoot must keep /etc/frameworks traversable for privateer TLS reads:\n%s", frameworksDirTask)
	}
	if strings.Contains(frameworksDirTask, `mode: "0750"`) {
		t.Fatalf("chatwoot must not make /etc/frameworks private to root; privateer needs to read /etc/frameworks/pki/ca.crt:\n%s", frameworksDirTask)
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile("../../../" + path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func nilHost() inventory.Host {
	return inventory.Host{}
}
