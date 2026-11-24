package provisioner

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ingress"
	"gopkg.in/yaml.v3"
)

const nginxRoleDir = "ansible/collections/ansible_collections/frameworks/infra/roles/nginx"

var nginxCredentialURISamples = map[string]string{
	"/webrtc/sk_live":          "/webrtc/REDACTED",
	"/whip/sk_live":            "/whip/REDACTED",
	"/ingest/sk_live?proto=1":  "/ingest/REDACTED?proto=1",
	"/sk_live.mkv":             "/REDACTED.mkv",
	"/view/sk_live.mkv":        "/view/REDACTED.mkv",
	"/sk_live.webm":            "/REDACTED.webm",
	"/hls/playback/index.m3u8": "/hls/playback/index.m3u8",
}

// nginxLoggedURI evaluates the rendered `map $request_uri
// $frameworks_logged_uri` block the way Nginx does: the first matching regex
// key wins and its value expands the named captures.
func nginxLoggedURI(t *testing.T, config, uri string) string {
	t.Helper()
	block := regexp.MustCompile(`(?s)map \$request_uri \$frameworks_logged_uri \{(.*?)\n\s*\}`).FindStringSubmatch(config)
	if block == nil {
		t.Fatalf("rendered config has no $frameworks_logged_uri map:\n%s", config)
	}
	entries := regexp.MustCompile(`"~([^"]+)" "([^"]+)";`).FindAllStringSubmatch(block[1], -1)
	if len(entries) == 0 {
		t.Fatalf("$frameworks_logged_uri map has no regex entries:\n%s", block[1])
	}
	for _, e := range entries {
		re := regexp.MustCompile(e[1])
		if m := re.FindStringSubmatchIndex(uri); m != nil {
			return string(re.ExpandString(nil, e[2], uri, m))
		}
	}
	return uri
}

func assertNginxRedactsCredentials(t *testing.T, name, config string) {
	t.Helper()
	for uri, want := range nginxCredentialURISamples {
		if got := nginxLoggedURI(t, config, uri); got != want {
			t.Errorf("%s: %s logged as %s, want %s", name, uri, got, want)
		}
	}
}

func TestRenderNginxRootConfigRedactsPublishingCredentials(t *testing.T) {
	assertNginxRedactsCredentials(t, "docker", renderNginxRootConfig("/etc/nginx/conf.d/frameworks.conf"))
}

func TestNativeNginxRootTemplateRedactsPublishingCredentials(t *testing.T) {
	var vars map[string]any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, nginxRoleDir+"/defaults/main.yml")), &vars); err != nil {
		t.Fatal(err)
	}
	vars["nginx_worker_user"] = "nginx"
	vars["nginx_module_includes"] = []string{}
	vars["nginx_effective_http_include_path"] = "/fixture/sites.conf"
	assertNginxRedactsCredentials(t, "native", renderRoleTemplate(t, "nginx/templates/nginx.conf.j2", vars))
}

func TestNginxRoleVarsPassCredentialURIMap(t *testing.T) {
	vars, err := nginxRoleVars(context.Background(), nilHost(), ServiceConfig{}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("nginxRoleVars: %v", err)
	}
	got, ok := vars["nginx_credential_uri_map"].([]map[string]any)
	want := ingress.NginxCredentialURIMap()
	if !ok || len(got) != len(want) {
		t.Fatalf("nginx_credential_uri_map = %#v, want the %d pkg/ingress entries", vars["nginx_credential_uri_map"], len(want))
	}
	for i, e := range want {
		if got[i]["regex"] != e.Regex || got[i]["value"] != e.Value {
			t.Fatalf("nginx_credential_uri_map[%d] = %#v, want %+v", i, got[i], e)
		}
	}
}

// The role default serves standalone and molecule runs; it must not drift
// from the pkg/ingress definition the CLI passes.
func TestNginxRoleDefaultCredentialURIMapMatchesIngress(t *testing.T) {
	var defaults struct {
		Map []struct {
			Regex string `yaml:"regex"`
			Value string `yaml:"value"`
		} `yaml:"nginx_credential_uri_map"`
	}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, nginxRoleDir+"/defaults/main.yml")), &defaults); err != nil {
		t.Fatalf("parse nginx defaults: %v", err)
	}
	want := ingress.NginxCredentialURIMap()
	if len(defaults.Map) != len(want) {
		t.Fatalf("nginx role default map has %d entries, want %d from pkg/ingress", len(defaults.Map), len(want))
	}
	for i, e := range want {
		if defaults.Map[i].Regex != e.Regex || defaults.Map[i].Value != e.Value {
			t.Fatalf("nginx role default entry %d = %+v, want %+v", i, defaults.Map[i], e)
		}
	}
	template := readRepoFile(t, nginxRoleDir+"/templates/nginx.conf.j2")
	if strings.Contains(template, "REDACTED") {
		t.Fatalf("nginx.conf.j2 hard-codes a redaction pattern instead of using nginx_credential_uri_map:\n%s", template)
	}
}

// credentialPathFixtureMetadata is a catch-all Foghorn site, a site that
// declares /whip/ itself, and tenant-alias playback.
func credentialPathFixtureMetadata() map[string]any {
	return map[string]any{
		"proxy_sites": []map[string]any{
			{"name": "foghorn", "domains": []string{"foghorn.example.com"}, "upstream": "127.0.0.1:18008", "profile": "api"},
			{"name": "whip", "domains": []string{"whip.example.com"}, "upstream": "127.0.0.1:18009", "profile": "api", "path_prefixes": []string{"/whip/"}},
		},
		"tenant_alias_playback": map[string]any{"root_domain": "example.com", "upstream_port": 18008},
	}
}

func assertCredentialPathErrorLogsDiscarded(t *testing.T, name, config string, carvedPerPrefix int) {
	t.Helper()
	for _, prefix := range []string{"/ingest/", "/webrtc/", "/whip/"} {
		carved := "    location ^~ " + prefix + " {\n        error_log /dev/null crit;\n"
		if got := strings.Count(config, carved); got != carvedPerPrefix {
			t.Errorf("%s: %d carved %s locations discard their error log, want %d:\n%s", name, got, prefix, carvedPerPrefix, config)
		}
	}
	if declared := "    location /whip/ {\n        error_log /dev/null crit;\n"; !strings.Contains(config, declared) {
		t.Errorf("%s: a site declaring /whip/ must discard that location's error log:\n%s", name, config)
	}
}

// Catch-all site and tenant-alias playback each carve every credential
// prefix; the /whip/-only site declares its own location instead.
func TestNginxCredentialPathLocationsCoverEveryIngressPrefix(t *testing.T) {
	metadata := credentialPathFixtureMetadata()
	docker := renderNginxConfig(80, normalizeProxySites(metadata, "docker"), tenantAliasPlaybackFromMetadata(metadata))
	assertCredentialPathErrorLogsDiscarded(t, "docker", docker, 2)

	var vars map[string]any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, nginxRoleDir+"/defaults/main.yml")), &vars); err != nil {
		t.Fatal(err)
	}
	roleVars, err := nginxRoleVars(context.Background(), nilHost(), ServiceConfig{Port: 18090, Metadata: metadata}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("nginxRoleVars: %v", err)
	}
	for k, v := range roleVars {
		vars[k] = v
	}
	vars["nginx_effective_http2_directive_mode"] = "standalone"
	native := renderRoleTemplate(t, "nginx/templates/frameworks.conf.j2", vars)
	assertCredentialPathErrorLogsDiscarded(t, "native", native, 2)
}

func TestNginxRoleCredentialPathPrefixesMatchIngress(t *testing.T) {
	want := ingress.CredentialPathPrefixes()
	vars, err := nginxRoleVars(context.Background(), nilHost(), ServiceConfig{}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("nginxRoleVars: %v", err)
	}
	if got, ok := vars["nginx_credential_path_prefixes"].([]string); !ok || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("nginx_credential_path_prefixes = %#v, want %v", vars["nginx_credential_path_prefixes"], want)
	}
	var defaults struct {
		Prefixes []string `yaml:"nginx_credential_path_prefixes"`
	}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, nginxRoleDir+"/defaults/main.yml")), &defaults); err != nil {
		t.Fatal(err)
	}
	if strings.Join(defaults.Prefixes, ",") != strings.Join(want, ",") {
		t.Fatalf("nginx role default credential prefixes = %v, want %v from pkg/ingress", defaults.Prefixes, want)
	}
	if template := readRepoFile(t, nginxRoleDir+"/templates/frameworks.conf.j2"); strings.Contains(template, "'/webrtc") || strings.Contains(template, "'/ingest") {
		t.Fatalf("frameworks.conf.j2 hard-codes credential prefixes instead of using nginx_credential_path_prefixes")
	}
}
