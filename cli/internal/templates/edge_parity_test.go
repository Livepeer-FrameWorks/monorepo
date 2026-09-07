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
// operators rely on: same single edge service, same volume layout, same
// env keys. Catches drift between the two template surfaces without
// requiring a full role reimplementation here.
func TestEdgeTemplateParity(t *testing.T) {
	t.Parallel()

	// Render the Go-side templates against a fixed set of vars (linux
	// flavor — the shape ansible applies remotely).
	tmpDir := t.TempDir()
	vars := EdgeVars{
		NodeID:          "parity-node",
		EdgeDomain:      "edge.parity.test",
		SiteAddress:     "edge.parity.test",
		AcmeEmail:       "ops@parity.test",
		FoghornGRPCAddr: "foghorn.parity.test:18008",
		EnrollmentToken: "parity-token",
		Mode:            "container",
		EdgeOS:          "linux",
	}
	if err := WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		t.Fatalf("WriteEdgeTemplates: %v", err)
	}

	goCompose := readFile(t, filepath.Join(tmpDir, "docker-compose.edge.yml"))
	goEnv := readFile(t, filepath.Join(tmpDir, ".edge.env"))

	// Locate the paired Jinja templates and read them raw — we don't render
	// them here (no Jinja), but we assert invariant substrings on the
	// source so renames/deletions on either side fail the test.
	jinjaCompose := readFile(t, ansibleTemplatePath(t, "compose.yml.j2"))
	jinjaEdgeEnv := readFile(t, ansibleTemplatePath(t, "edge.env.j2"))
	nativeLinux := readFile(t, ansibleEdgeRolePath(t, "tasks/install-native-linux.yml"))
	nativeDarwin := readFile(t, ansibleEdgeRolePath(t, "tasks/install-native-darwin.yml"))

	// One edge service on both surfaces, host networking, same container
	// name, same persistent volumes.
	wantComposeBits := []string{
		"edge:",
		"container_name: frameworks-edge",
		"network_mode: host",
		"frameworks_opt:/opt/frameworks",
		"frameworks_etc:/etc/frameworks",
		"./pki:/etc/frameworks/pki",
		"./.edge-enroll.env:/run/frameworks/.edge-enroll.env",
		"./.edge.env:/run/frameworks/.edge.env",
		"caddy_etc:/etc/caddy",
		"caddy_data:/var/lib/caddy",
		"shm_size",
		"stop_grace_period",
	}
	for _, needle := range wantComposeBits {
		if !strings.Contains(goCompose, needle) {
			t.Errorf("go compose missing %q", needle)
		}
		if !strings.Contains(jinjaCompose, needle) {
			t.Errorf("jinja compose missing %q", needle)
		}
	}
	// Hot storage intentionally diverges: Ansible keeps the ./storage host
	// bind so nodes migrated from the retired 3-container stack keep their
	// DVR/clip artifacts (same bind path); the operator-local renderer uses
	// a named volume (no legacy data, better macOS IO). Both mount
	// /data/storage in the container.
	if !strings.Contains(goCompose, "edge_storage:/data/storage") {
		t.Error("go compose must mount the edge_storage named volume at /data/storage")
	}
	if !strings.Contains(jinjaCompose, "./storage:/data/storage") {
		t.Error("jinja compose must keep the ./storage host bind at /data/storage (legacy-stack migration continuity)")
	}
	if !strings.Contains(goCompose, "edge_state:/data/state") {
		t.Error("go compose must mount durable Helmsman state independently at /data/state")
	}
	if !strings.Contains(jinjaCompose, "./state:/data/state") {
		t.Error("jinja compose must keep durable Helmsman state in the stable ./state host bind")
	}
	// The retired 3-container services must not resurface.
	for _, legacy := range []string{"edge-proxy", "caddy_admin", "mist_thumbs"} {
		if strings.Contains(goCompose, legacy) {
			t.Errorf("go compose still references legacy %q", legacy)
		}
		if strings.Contains(jinjaCompose, legacy) {
			t.Errorf("jinja compose still references legacy %q", legacy)
		}
	}

	wantEnvKeys := []string{
		"NODE_ID", "EDGE_DOMAIN", "FOGHORN_CONTROL_ADDR", "DEPLOY_MODE", "TELEMETRY_URL", "HELMSMAN_ROTATE_NODE_IDENTITY",
		"RESTREAM_ALLOW_PRIVATE_DESTINATIONS", "RESTREAM_ALLOWED_PRIVATE_CIDRS", "RESTREAM_DENIED_CIDRS",
	}
	for _, key := range wantEnvKeys {
		if !strings.Contains(goEnv, key+"=") {
			t.Errorf("go .edge.env missing key %q", key)
		}
		if !strings.Contains(jinjaEdgeEnv, key+"=") {
			t.Errorf("jinja edge.env missing key %q", key)
		}
		if strings.HasPrefix(key, "RESTREAM_") {
			for _, surface := range []struct{ name, content string }{
				{"native linux", nativeLinux},
				{"native darwin", nativeDarwin},
			} {
				if got := strings.Count(surface.content, key+":"); got != 4 {
					t.Errorf("%s must pass %q through all four native Helmsman lifecycle paths; got %d occurrences", surface.name, key, got)
				}
			}
		}
	}
	if !strings.Contains(goEnv, "DEPLOY_MODE=container") {
		t.Error("go .edge.env must declare DEPLOY_MODE=container")
	}
	if !strings.Contains(jinjaEdgeEnv, "'container'") {
		t.Error("jinja edge.env must normalize DEPLOY_MODE to container")
	}

	// The enrollment token is split into a write-once env file on both
	// surfaces so a fresh token on re-provision never changes .edge.env
	// (compose recreates the edge container on env changes).
	if strings.Contains(goEnv, "EDGE_ENROLLMENT_TOKEN") {
		t.Error("go .edge.env must not carry the enrollment token")
	}
	if strings.Contains(jinjaEdgeEnv, "EDGE_ENROLLMENT_TOKEN") {
		t.Error("jinja edge.env must not carry the enrollment token")
	}
	goEnroll := readFile(t, filepath.Join(tmpDir, ".edge-enroll.env"))
	if !strings.Contains(goEnroll, "EDGE_ENROLLMENT_TOKEN=parity-token") {
		t.Error("go .edge-enroll.env missing the enrollment token")
	}
	for _, surface := range []struct{ name, content string }{
		{"go compose", goCompose},
		{"jinja compose", jinjaCompose},
	} {
		if !strings.Contains(surface.content, ".edge-enroll.env") {
			t.Errorf("%s must load the write-once enrollment env file", surface.name)
		}
		// The Mist password rides in the 0600 secrets env file, never
		// inline in the world-readable compose file.
		if !strings.Contains(surface.content, ".edge-secrets.env") {
			t.Errorf("%s must load the secrets env file", surface.name)
		}
		if strings.Contains(surface.content, "MIST_API_PASSWORD=") {
			t.Errorf("%s must not inline the Mist API password", surface.name)
		}
	}

	// Container mode renders no host-side Caddyfile: the bootstrap config
	// is baked into the edge image and helmsman persists the activated one
	// on the caddy_etc volume.
	if _, err := os.Stat(filepath.Join(tmpDir, "Caddyfile")); err == nil {
		t.Error("container mode must not render a host-side Caddyfile")
	}

	// Darwin flavor publishes the bounded media port set instead of host
	// networking (Docker Desktop UDP host networking is broken on macOS).
	darwinDir := t.TempDir()
	darwinVars := vars
	darwinVars.EdgeOS = "darwin"
	if err := WriteEdgeTemplates(darwinDir, darwinVars, true); err != nil {
		t.Fatalf("WriteEdgeTemplates (darwin): %v", err)
	}
	darwinCompose := readFile(t, filepath.Join(darwinDir, "docker-compose.edge.yml"))
	for _, needle := range []string{`"18203:18203/udp"`, `"8889:8889/udp"`, `"1935:1935"`, "edge-tuning", "privileged: true"} {
		if !strings.Contains(darwinCompose, needle) {
			t.Errorf("darwin compose missing %q", needle)
		}
	}
	if strings.Contains(darwinCompose, "network_mode: host\n    shm_size") {
		t.Error("darwin edge service must not use host networking")
	}
}

func TestContainerCredentialScrubberRunsAsNarrowRootHelper(t *testing.T) {
	t.Parallel()
	runPath := repositoryPath(t, "edge/rootfs/etc/s6-overlay/s6-rc.d/credential-scrubber/run")
	run := readFile(t, runPath)
	if !strings.Contains(run, "helmsman scrub-edge-credentials") {
		t.Fatalf("credential scrubber must invoke the narrow Helmsman command:\n%s", run)
	}
	if strings.Contains(run, "s6-setuidgid") {
		t.Fatalf("credential scrubber must retain root for root-owned bind mounts:\n%s", run)
	}
	info, err := os.Stat(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("credential scrubber run script is not executable: %v", info.Mode())
	}
	for _, path := range []string{
		"edge/rootfs/etc/s6-overlay/s6-rc.d/credential-scrubber/dependencies.d/init-seed",
		"edge/rootfs/etc/s6-overlay/s6-rc.d/user/contents.d/credential-scrubber",
	} {
		if _, err := os.Stat(repositoryPath(t, path)); err != nil {
			t.Fatalf("credential scrubber service wiring %s: %v", path, err)
		}
	}
	seed := readFile(t, repositoryPath(t, "api_sidecar/internal/edgeseed/seed.go"))
	if strings.Contains(seed, ".edge-enroll.env") || strings.Contains(seed, "HELMSMAN_ENROLLMENT_TOKEN_FILE") {
		t.Fatal("root init-seed must not change ownership or mode of the host credential bind mount")
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

func ansibleEdgeRolePath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))
	p := filepath.Join(repoRoot, "ansible/collections/ansible_collections/frameworks/infra/roles/edge", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("missing edge role file %s: %v", p, err)
	}
	return p
}

func repositoryPath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))
	return filepath.Join(repoRoot, name)
}
