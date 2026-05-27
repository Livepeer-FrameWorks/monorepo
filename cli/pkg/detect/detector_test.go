package detect

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// fakeRunner records calls and returns scripted responses per command prefix.
type fakeRunner struct {
	responses []fakeResponse
	calls     []string
}

type fakeResponse struct {
	matchPrefix string
	exitCode    int
	stdout      string
	stderr      string
}

func (f *fakeRunner) runSSH(_ context.Context, cmd string) (int, string, string) {
	f.calls = append(f.calls, cmd)
	for _, r := range f.responses {
		if r.matchPrefix == "" || startsWith(cmd, r.matchPrefix) {
			return r.exitCode, r.stdout, r.stderr
		}
	}
	return -1, "", "no response configured"
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func newDetectorWithRunner(host inventory.Host, r sshRunner) *Detector {
	return &Detector{host: host, runner: r}
}

// TestDetect_InventoryMissServiceFoundInDocker verifies that a non-zero exit
// on the inventory check does not abort the chain — the detector must fall
// through to the docker probe.
func TestDetect_InventoryMissServiceFoundInDocker(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1, stderr: "No such file"},
			{matchPrefix: "docker ps -a", exitCode: 0, stdout: "frameworks-foghorn|running|foghorn:v1"},
			{matchPrefix: "docker inspect", exitCode: 0, stdout: "true"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "foghorn")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !state.Exists {
		t.Fatalf("expected Exists=true after docker probe succeeded")
	}
	if state.Mode != "docker" || !state.Running {
		t.Fatalf("got mode=%q running=%v, want docker/true", state.Mode, state.Running)
	}
	if state.Version != "v1" {
		t.Fatalf("version=%q, want v1", state.Version)
	}
}

func TestDetect_DockerRequiresExactContainerName(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1, stderr: "No such file"},
			{
				matchPrefix: "docker ps -a --filter name=frameworks-chatwoot ",
				exitCode:    0,
				stdout:      "frameworks-chatwoot|running|chatwoot/chatwoot:v3.14.0\nframeworks-chatwoot-worker|running|chatwoot/chatwoot:v3.14.0",
			},
			{matchPrefix: "docker inspect", exitCode: 0, stdout: "true"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "chatwoot")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Metadata["container_name"] != "frameworks-chatwoot" {
		t.Fatalf("container_name=%q", state.Metadata["container_name"])
	}
	if state.Version != "v3.14.0" {
		t.Fatalf("version=%q, want v3.14.0", state.Version)
	}
}

func TestDetect_DockerVersionFromDigestPinnedImage(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1, stderr: "No such file"},
			{
				matchPrefix: "docker ps -a --filter name=frameworks-nginx ",
				exitCode:    0,
				stdout:      "frameworks-nginx|running|nginx:1.29.3-alpine@sha256:abcdef",
			},
			{matchPrefix: "docker inspect", exitCode: 0, stdout: "true"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "nginx")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Version != "1.29.3-alpine" {
		t.Fatalf("version=%q, want 1.29.3-alpine", state.Version)
	}
}

func TestDockerImageVersion(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"foghorn:v0.2.69": "v0.2.69",
		"ghcr.io/livepeer-frameworks/foghorn:v0.2.69":        "v0.2.69",
		"nginx:1.29.3-alpine@sha256:abcdef":                  "1.29.3-alpine",
		"registry.local:5000/livepeer/foghorn:v0.2.69":       "v0.2.69",
		"registry.local:5000/livepeer/foghorn@sha256:abcdef": "",
		"registry.local:5000/livepeer/foghorn":               "",
		"sha256:abcdef":                                      "",
	}
	for image, want := range tests {
		if got := dockerImageVersion(image); got != want {
			t.Fatalf("dockerImageVersion(%q)=%q, want %q", image, got, want)
		}
	}
}

// TestDetect_AllMethodsFailReturnsNotFound verifies that exhaustively failing
// probes yield Exists=false rather than bubbling up an error.
func TestDetect_AllMethodsFailReturnsNotFound(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "", exitCode: 1, stderr: "fail"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "some-service")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Exists {
		t.Fatalf("expected Exists=false when every probe returned non-zero")
	}
}

// TestDetect_NonZeroExitIsNotHardFailure pins the contract: a non-zero exit
// from one method must NOT short-circuit; the detector moves to the next.
func TestDetect_NonZeroExitIsNotHardFailure(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1},
			{matchPrefix: "docker ps -a", exitCode: 1},
			{matchPrefix: "systemctl show", exitCode: 0, stdout: "LoadState=loaded\nActiveState=active\nSubState=running"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "foghorn")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !state.Exists || state.Mode != "native" || !state.Running {
		t.Fatalf("got %+v, want native service detected as running", state)
	}
	// Chain must have progressed past inventory and docker to systemd.
	if len(r.calls) < 3 {
		t.Fatalf("expected the detector to try multiple methods, got %d calls", len(r.calls))
	}
}

func TestDetect_SystemdReadsNativePlatformVersion(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1},
			{matchPrefix: "docker ps -a", exitCode: 1},
			{
				matchPrefix: "systemctl show",
				exitCode:    0,
				stdout: strings.Join([]string{
					"LoadState=loaded",
					"ActiveState=active",
					"SubState=running",
					"ExecStart={ path=/opt/frameworks/quartermaster/quartermaster ; argv[]=/opt/frameworks/quartermaster/quartermaster serve ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }",
				}, "\n"),
			},
			{
				matchPrefix: "'/opt/frameworks/quartermaster/quartermaster' version --json",
				exitCode:    0,
				stdout:      `{"version":"v0.2.32","component_name":"quartermaster","component_version":"0.2.0"}`,
			},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "quartermaster")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Version != "v0.2.32" {
		t.Fatalf("version=%q, want v0.2.32", state.Version)
	}
	if state.Metadata["binary_path"] != "/opt/frameworks/quartermaster/quartermaster" {
		t.Fatalf("binary_path=%q", state.Metadata["binary_path"])
	}
}
