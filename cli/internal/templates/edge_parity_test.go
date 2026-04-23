package templates

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestEdgeTemplateParity asserts the operator-local renderer in this
// package and the Ansible role's Jinja templates agree on the shape
// operators rely on: same service names, same bootstrap Caddyfile
// semantics, same env keys. Catches drift between the two template
// surfaces without requiring a full role reimplementation here.
func TestEdgeTemplateParity(t *testing.T) {
	t.Parallel()

	// Render the Go-side templates against a fixed set of vars.
	tmpDir := t.TempDir()
	vars := EdgeVars{
		NodeID:          "parity-node",
		EdgeDomain:      "edge.parity.test",
		SiteAddress:     "edge.parity.test",
		AcmeEmail:       "ops@parity.test",
		FoghornGRPCAddr: "foghorn.parity.test:18008",
		EnrollmentToken: "parity-token",
		Mode:            "docker",
	}
	if err := WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		t.Fatalf("WriteEdgeTemplates: %v", err)
	}

	goCompose := readFile(t, filepath.Join(tmpDir, "docker-compose.edge.yml"))
	goCaddyfile := readFile(t, filepath.Join(tmpDir, "Caddyfile"))
	goEnv := readFile(t, filepath.Join(tmpDir, ".edge.env"))

	// Locate the paired Jinja templates and read them raw — we don't render
	// them here (no Jinja), but we assert invariant substrings on the
	// source so renames/deletions on either side fail the test.
	jinjaCompose := readFile(t, ansibleTemplatePath(t, "compose.yml.j2"))
	jinjaCaddyfileDocker := readFile(t, ansibleTemplatePath(t, "Caddyfile.docker.j2"))
	jinjaEdgeEnv := readFile(t, ansibleTemplatePath(t, "edge.env.j2"))

	wantServiceNames := []string{"caddy", "mistserver", "helmsman"}
	for _, svc := range wantServiceNames {
		if !strings.Contains(goCompose, svc+":") {
			t.Errorf("go compose missing service %q", svc)
		}
		if !strings.Contains(jinjaCompose, svc+":") {
			t.Errorf("jinja compose missing service %q", svc)
		}
	}

	wantBootstrapBits := []string{"tls internal", "503"}
	for _, needle := range wantBootstrapBits {
		if !strings.Contains(goCaddyfile, needle) {
			t.Errorf("go Caddyfile missing bootstrap marker %q", needle)
		}
		if !strings.Contains(jinjaCaddyfileDocker, needle) {
			t.Errorf("jinja Caddyfile.docker missing bootstrap marker %q", needle)
		}
	}

	wantEnvKeys := []string{"NODE_ID", "EDGE_DOMAIN", "FOGHORN_CONTROL_ADDR", "EDGE_ENROLLMENT_TOKEN", "DEPLOY_MODE"}
	for _, key := range wantEnvKeys {
		if !strings.Contains(goEnv, key+"=") {
			t.Errorf("go .edge.env missing key %q", key)
		}
		if !strings.Contains(jinjaEdgeEnv, key+"=") {
			t.Errorf("jinja edge.env missing key %q", key)
		}
	}

	// Webhook upstream wiring must match mode: docker → helmsman:18007,
	// native → localhost:18007. Both source surfaces must agree.
	if !strings.Contains(goCaddyfile, "helmsman:18007") {
		t.Error("go Caddyfile (docker) should proxy webhooks to helmsman:18007")
	}
	if !strings.Contains(jinjaCaddyfileDocker, "helmsman:18007") {
		t.Error("jinja Caddyfile.docker should proxy webhooks to helmsman:18007")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ansibleTemplatePath resolves a Jinja template in the edge role relative
// to the test file, so the parity test works from any CWD.
func ansibleTemplatePath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// cli/internal/templates/edge_parity_test.go → repo root
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))
	p := filepath.Join(repoRoot, "ansible/collections/ansible_collections/frameworks/infra/roles/edge/templates", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("missing jinja template %s: %v", p, err)
	}
	return p
}
