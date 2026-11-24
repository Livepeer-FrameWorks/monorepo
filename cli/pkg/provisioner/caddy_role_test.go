package provisioner

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ingress"
	"gopkg.in/yaml.v3"
)

const caddyRoleDir = "ansible/collections/ansible_collections/frameworks/infra/roles/caddy"

var credentialURISamples = map[string]string{
	"/webrtc/sk_live":          "/webrtc/REDACTED",
	"/ingest/sk_live?proto=1":  "/ingest/REDACTED?proto=1",
	"/sk_live.mkv":             "/REDACTED.mkv",
	"/view/sk_live.mkv":        "/view/REDACTED.mkv",
	"/sk_live.webm":            "/REDACTED.webm",
	"/hls/playback/index.m3u8": "/hls/playback/index.m3u8",
}

func TestRenderCaddyfileRedactsPublishingCredentialsFromLogs(t *testing.T) {
	for name, content := range map[string]string{
		"no sites": renderCaddyfile(nil),
		"sites":    renderCaddyfile([]proxySite{{Domains: []string{"foghorn.example.com"}, Upstream: "127.0.0.1:18008"}}),
	} {
		line := regexp.MustCompile(`request>uri regexp "([^"]+)" "([^"]+)"`)
		matches := line.FindAllStringSubmatch(content, -1)
		if len(matches) != 1 {
			t.Fatalf("%s: want one global request>uri filter, got %d:\n%s", name, len(matches), content)
		}
		re := regexp.MustCompile(matches[0][1])
		for uri, want := range credentialURISamples {
			if got := re.ReplaceAllString(uri, matches[0][2]); got != want {
				t.Errorf("%s: %s logged as %s, want %s", name, uri, got, want)
			}
		}
	}
}

type caddyRoleRedaction struct {
	Regexp      string `yaml:"regexp"`
	Replacement string `yaml:"replacement"`
}

func TestCaddyRoleVarsPassCredentialURIRedaction(t *testing.T) {
	vars, err := caddyRoleVars(context.Background(), nilHost(), ServiceConfig{}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("caddyRoleVars: %v", err)
	}
	got, ok := vars["caddy_credential_uri_redaction"].(map[string]any)
	if !ok {
		t.Fatalf("caddy_credential_uri_redaction missing from role vars: %#v", vars)
	}
	if got["regexp"] != ingress.CredentialURIRegexp || got["replacement"] != ingress.CredentialURIReplacement {
		t.Fatalf("caddy_credential_uri_redaction = %#v, want pkg/ingress definition", got)
	}
}

// The role default serves standalone and molecule runs; it must not drift
// from the pkg/ingress definition the CLI passes.
func TestCaddyRoleDefaultCredentialURIRedactionMatchesIngress(t *testing.T) {
	var defaults struct {
		Redaction caddyRoleRedaction `yaml:"caddy_credential_uri_redaction"`
	}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, caddyRoleDir+"/defaults/main.yml")), &defaults); err != nil {
		t.Fatalf("parse caddy defaults: %v", err)
	}
	if defaults.Redaction.Regexp != ingress.CredentialURIRegexp || defaults.Redaction.Replacement != ingress.CredentialURIReplacement {
		t.Fatalf("caddy role default redaction = %+v, want regexp %q replacement %q",
			defaults.Redaction, ingress.CredentialURIRegexp, ingress.CredentialURIReplacement)
	}

	template := readRepoFile(t, caddyRoleDir+"/templates/Caddyfile.j2")
	const want = `request>uri regexp "{{ caddy_credential_uri_redaction.regexp }}" "{{ caddy_credential_uri_redaction.replacement }}"`
	if !strings.Contains(template, want) {
		t.Fatalf("Caddyfile.j2 must render the role's redaction var as %q:\n%s", want, template)
	}
	if strings.Contains(template, "REDACTED") {
		t.Fatalf("Caddyfile.j2 hard-codes a redaction pattern instead of using the role var:\n%s", template)
	}
}
