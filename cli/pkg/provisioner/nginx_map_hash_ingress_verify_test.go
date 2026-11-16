//go:build ingress_verify

package provisioner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestNginxRootConfigAcceptsMaximumLengthAlias(t *testing.T) {
	image := os.Getenv("FRAMEWORKS_NGINX_TEST_IMAGE")
	if image == "" {
		t.Fatal("run through make verify-hosted-alias-ingress for the pinned image")
	}
	host := "foghorn." + strings.Repeat("a", 63) + ".cdn." + strings.Repeat("r", 63) + "." + strings.Repeat("s", 63) + "." + strings.Repeat("t", 49)
	if len(host) != 253 {
		t.Fatalf("fixture hostname has %d characters, want 253", len(host))
	}
	var nativeVars map[string]any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, testRolesDir+"nginx/defaults/main.yml")), &nativeVars); err != nil {
		t.Fatal(err)
	}
	nativeVars["nginx_worker_user"] = "nginx"
	nativeVars["nginx_module_includes"] = []string{}
	nativeVars["nginx_effective_http_include_path"] = "/fixture/sites.conf"
	configs := map[string]string{
		"docker": renderNginxRootConfig("/fixture/sites.conf"),
		"native": renderRoleTemplate(t, "nginx/templates/nginx.conf.j2", nativeVars),
	}
	for name, config := range configs {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "nginx.conf")
			sites := filepath.Join(dir, "sites.conf")
			if err := os.WriteFile(root, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			content := "map $ssl_server_name $fw_tenant_alias_bundle { default \"\"; " + host + " revisions/tenant/v1; }\nserver { listen 8080; return 204; }\n"
			if err := os.WriteFile(sites, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			output, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--read-only", "--tmpfs", "/tmp", "--tmpfs", "/run", "--tmpfs", "/var/cache/nginx", "-v", root+":/etc/nginx/nginx.conf:ro", "-v", sites+":/fixture/sites.conf:ro", image, "nginx", "-t").CombinedOutput()
			if err != nil {
				t.Fatalf("rendered Nginx config rejects a valid 253-character tenant hostname: %v\n%s", err, output)
			}
		})
	}
}
